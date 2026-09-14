# -*- coding: utf-8 -*-
"""Offline lexical-retrieval comparison: character 3-gram vs CRF segmentation.

Both representations are indexed as tsvector('simple', ...) in a throwaway
local PostgreSQL schema and queried with the same to_tsquery + ts_rank_cd
shape the service uses, so only the segmentation differs.
"""
import json, os, pathlib, subprocess, unicodedata

import khmercut

KIT = pathlib.Path(os.environ.get("KHMER_KIT", "/tmp/khmer-kit"))
DSN = os.environ.get("SEG_COMPARE_DSN", "postgres://postgres@/postgres?host=/tmp&port=55434")
KM = "០១២៣៤៥៦៧៨៩"
ZW = "\u200b\u200c\u200d\ufeff\u00a0"

def normalize(text):
    out = []
    pending = False
    for ch in text:
        if ch in KM:
            ch = str(KM.index(ch))
        if ch in ZW:
            ch = " "
        if ch in " \t\n\r\v\f":
            if out:
                pending = True
            continue
        if pending:
            out.append(" ")
            pending = False
        out.append(ch)
    return unicodedata.normalize("NFC", "".join(out).strip())

def is_cjk(r):
    o = ord(r)
    return 0x3400 <= o <= 0x4DBF or 0x4E00 <= o <= 0x9FFF

def is_khmer(r):
    return 0x1780 <= ord(r) <= 0x17FF

def segment_piece(piece):
    words, grams = [], []
    run, kind = [], None
    def flush():
        nonlocal run, kind
        if not run:
            return
        if kind in ("cjk", "km"):
            n = 2 if kind == "cjk" else 3
            seen = set()
            if len(run) >= n:
                for i in range(len(run) - n + 1):
                    g = "".join(run[i:i+n])
                    if g not in seen:
                        seen.add(g)
                        grams.append(g)
            elif len(run) >= 2:
                grams.append("".join(run))
        else:
            words.append("".join(run))
        run = []
    for ch in piece:
        k = "cjk" if is_cjk(ch) else ("km" if is_khmer(ch) else ("plain" if ch.isalnum() else None))
        if k is None:
            flush()
            kind = None
            continue
        if run and k != kind:
            flush()
        kind = k
        run.append(ch)
    flush()
    return words, grams

def ngram_doc(text):
    tokens = []
    for piece in normalize(text).split():
        w, g = segment_piece(piece)
        tokens.extend(w)
        tokens.extend(g)
    return " ".join(tokens)

def ngram_query_tsq(text):
    terms = []
    for piece in normalize(text).split():
        w, g = segment_piece(piece)
        for x in w:
            x = "".join(c for c in x if c.isalnum() or c == "_")
            if x:
                terms.append(x)
        g = g[:24]
        if g:
            terms.append("(" + " | ".join(g) + ")")
    return " | ".join(t for t in terms if t)

def _keep_char(ch):
    # Letters + marks + numbers: Khmer combining signs are category Mn and
    # "isalnum()" is False for them, but dropping them destroys the word.
    return unicodedata.category(ch)[0] in ("L", "M", "N")

def _clean_tokens(tokens):
    out = []
    for tok in tokens:
        cur = []
        for ch in tok:
            if _keep_char(ch):
                cur.append(ch)
            else:
                if cur:
                    out.append("".join(cur))
                    cur = []
        if cur:
            out.append("".join(cur))
    return out

def crf_doc(text):
    return " ".join(_clean_tokens(khmercut.tokenize(normalize(text))))

def crf_query_tsq(text, join=" | "):
    toks = _clean_tokens(khmercut.tokenize(normalize(text)))
    return join.join(toks)

DOCS_DIR = KIT / "kb"
EVAL = json.loads((KIT / "rag_eval.json").read_text(encoding="utf-8"))["queries"]

def psql(sql):
    r = subprocess.run(["psql", DSN, "-tA", "-v", "ON_ERROR_STOP=1", "-c", sql],
                       capture_output=True, text=True)
    if r.returncode == 0:
        return r.stdout
    raise RuntimeError("psql failed: " + r.stderr[:400])

def esc(s):
    return s.replace("'", "''")

def setup_db():
    psql("DROP TABLE IF EXISTS knowledge_chunks; DROP TABLE IF EXISTS knowledge_documents;")
    psql("CREATE TABLE knowledge_documents (doc_id int primary key, title text)")
    psql("CREATE TABLE knowledge_chunks (chunk_id serial primary key, doc_id int, "
         "content text, content_seg text, content_seg_crf text, "
         "content_tsv tsvector, content_tsv_crf tsvector)")
    files = sorted(DOCS_DIR.glob("*.md"))
    assert len(files) == 14, len(files)
    for i, path in enumerate(files):
        doc_id = 14 + i
        content = normalize(path.read_text(encoding="utf-8"))
        seg_ng = ngram_doc(content)
        seg_crf = crf_doc(content)
        psql("INSERT INTO knowledge_documents VALUES (%d, '%s');"
             "INSERT INTO knowledge_chunks (doc_id, content, content_seg, content_seg_crf, content_tsv, content_tsv_crf) "
             "VALUES (%d, '%s', '%s', '%s', to_tsvector('simple', '%s'), to_tsvector('simple', '%s'));" % (
                 doc_id, esc(path.stem), doc_id, esc(content), esc(seg_ng), esc(seg_crf), esc(seg_ng), esc(seg_crf)))
    return len(files)

def ranked_docs(column, tsq):
    sql = ("SELECT doc_id FROM knowledge_chunks WHERE %s @@ to_tsquery('simple', '%s') "
           "ORDER BY ts_rank_cd(%s, to_tsquery('simple', '%s')) DESC, chunk_id ASC LIMIT 10") % (
               column, esc(tsq), column, esc(tsq))
    ids, seen = [], set()
    for line in psql(sql).splitlines():
        v = int(line.strip())
        if v not in seen:
            seen.add(v)
            ids.append(v)
    return ids

def evaluate(name, column, builder):
    hits5 = hits10 = 0
    mrr = 0.0
    misses = []
    for q in EVAL:
        tsq = builder(q["query"])
        if not tsq:
            misses.append(q["query"])
            continue
        ids = ranked_docs(column, tsq)
        best = 0
        for i, did in enumerate(ids):
            if did in q["expect"]:
                best = i + 1
                break
        if best > 0 and best <= 5:
            hits5 += 1
        if best > 0:
            hits10 += 1
            mrr += 1.0 / best
        else:
            misses.append(q["query"])
    n = len(EVAL)
    print("%-10s recall@5 %2d/%d (%3.0f%%)  recall@10 %2d/%d  MRR %.3f" % (
        name, hits5, n, 100.0 * hits5 / n, hits10, n, mrr / n))
    return misses

def main():
    print("docs indexed:", setup_db())
    m_ng = evaluate("ngram-OR", "content_tsv", ngram_query_tsq)
    m_crf = evaluate("crf-OR", "content_tsv_crf", lambda q: crf_query_tsq(q, " | "))
    evaluate("crf-AND", "content_tsv_crf", lambda q: crf_query_tsq(q, " & "))
    for label, misses in (("ngram", m_ng), ("crf", m_crf)):
        print("\n%s misses (%d):" % (label, len(misses)))
        for q in misses:
            print("  -", q[:60])

if __name__ == "__main__":
    main()
