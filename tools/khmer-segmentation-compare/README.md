# Khmer segmentation: CRF vs character 3-gram (retrieval comparison)

One-off offline experiment: index the same Khmer KB with two lexical
representations, query with the same `to_tsquery` + `ts_rank_cd` shape the
service uses, and compare recall/MRR.

- 3-gram: the production representation (`rag.SegmentForSearch`, migration 058)
- CRF: [khmercut](https://github.com/seanghay/khmercut-rs) (MIT; model from the
  khmer-nltk lineage), installed with `pip install khmercut`

## Reproduce

```bash
# local throwaway PostgreSQL (no extensions needed)
initdb -D /tmp/pgseg -U postgres --no-locale -E UTF8
pg_ctl -D /tmp/pgseg -o "-p 55434 -k /tmp -c listen_addresses=''" -l /tmp/pgseg.log start
cd tools/khmer-segmentation-compare
KHMER_KIT=/tmp/khmer-kit python3 seg_compare.py
```

`KHMER_KIT` must contain `kb/*.md` (14 docs) and `rag_eval.json`
(40 queries with expected doc ids 14..27; generated from `questions.json` by
`/tmp/khmer-kit/gen.py` topic -> doc-id mapping).

## Results (2026-09-14)

| representation | recall@5 | recall@10 | MRR@10 | lexemes/doc |
|---|---|---|---|---|
| 3-gram OR (current) | 40/40 (100%) | 40/40 | **0.942** | 170 |
| CRF OR | 39/40 (98%) | 40/40 | 0.900 | 47 |
| CRF OR, question words dropped | 40/40 (100%) | 40/40 | 0.908 | 47 |
| CRF AND | 1/40 (2%) | — | 0.025 | 47 |
| CRF AND, question words dropped | 15/40 (38%) | — | 0.362 | 47 |

Conclusion: on this KB/test set CRF segmentation does **not** improve lexical
retrieval (same-or-better recall, lower MRR); its win is a ~3.3x smaller index.
Keep the 3-gram representation; revisit CRF only for word-level features
(synonyms, knowledge-gap clustering) or if index size becomes a problem, and
always keep n-gram fallback for out-of-vocabulary names.
