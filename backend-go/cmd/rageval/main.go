// Command rageval — retrieval evaluation and RAG_* threshold calibration
// against a live database + embedding provider. Intended to run on the server
// (its DATABASE_URL and model_configs key are used in place, so production
// secrets never leave the host).
//
// ./rageval -eval /root/khmer-deploy/rag_eval.json -user 7 -limit 5
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/gemini"
	"khmer-ai-cs-go/internal/rag"
)

func main() {
	evalPath := flag.String("eval", "", "eval JSON: {\"queries\":[{\"query\":\"...\",\"expect\":[id]}]}")
	userID := flag.Int("user", 1, "tenant user id")
	limit := flag.Int("limit", 5, "topK")
	configsFlag := flag.String("configs", "0.35:0.75:0.70", "floor:ratio[:skip] triples")
	asJSON := flag.Bool("json", false, "print report as JSON")
	pipeline := flag.Bool("pipeline", false, "also run the production Search path (with rerank) and report it")
	flag.Parse()
	if *evalPath == "" {
		fmt.Fprintln(os.Stderr, "rageval: -eval is required")
		os.Exit(2)
	}
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "rageval: DATABASE_URL is not set")
		os.Exit(2)
	}

	raw, err := os.ReadFile(*evalPath)
	must(err)
	var doc struct {
		Queries []rag.EvalCase `json:"queries"`
	}
	must(json.Unmarshal(raw, &doc))
	if len(doc.Queries) == 0 {
		fmt.Fprintln(os.Stderr, "rageval: eval file has no queries")
		os.Exit(2)
	}
	configs, err := parseConfigs(*configsFlag)
	must(err)

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	must(err)
	defer pool.Close()
	svc := &rag.Service{DB: pool, Logger: slog.Default()}
	if apiKey, model, prompt, maxTokens, ok := gemini.LoadDefaultConfig(ctx, pool); ok {
		svc.Gemini = gemini.FromPartsFull(apiKey, model, prompt, maxTokens)
	} else {
		svc.Gemini = gemini.New(os.Getenv("GEMINI_API_KEY"), os.Getenv("GEMINI_MODEL"), 0)
	}
	if svc.Gemini.IsConfigured() == false {
		fmt.Fprintln(os.Stderr, "rageval: Gemini not configured (DB model_configs or GEMINI_API_KEY)")
		os.Exit(2)
	}

	report, err := svc.EvalRetrieval(ctx, int32(*userID), doc.Queries, configs, int64(*limit))
	must(err)
	if *asJSON {
		out, jerr := json.MarshalIndent(report, "", "  ")
		must(jerr)
		fmt.Println(string(out))
		return
	}
	if *pipeline {
		pm, avgSrc, perr := svc.EvalPipeline(ctx, int32(*userID), doc.Queries, int64(*limit))
		must(perr)
		fmt.Printf("PIPELINE recall@5 %d/%d  recall@10 %d/%d  MRR %.3f  avg_sources %.2f  (rerank skip from RAG_RERANK_SKIP=%v)\n",
			pm.Recall5, report.Cases, pm.Recall10, report.Cases, pm.MRR, avgSrc, os.Getenv("RAG_RERANK_SKIP"))
	}
	fmt.Printf("cases=%d limit=%d\n", report.Cases, report.Limit)
	n := float64(report.Cases)
	for _, leg := range report.Legs {
		fmt.Printf("LEG %-8s recall@5 %2d/%d (%3.0f%%)  recall@10 %2d/%d  MRR %.3f\n",
			leg.Name, leg.Recall5, report.Cases, 100*float64(leg.Recall5)/n, leg.Recall10, report.Cases, leg.MRR)
	}
	fmt.Printf("%-30s %-9s %-10s %-7s %-8s %-9s %s\n",
		"SWEEP", "recall@5", "recall@10", "MRR", "avg_src", "avg_cand", "rerank(skip/run)")
	for _, row := range report.Rows {
		fmt.Printf("floor=%.2f ratio=%.2f skip=%.2f   %2d/%d      %2d/%d      %.3f   %-8.2f %-9.2f %d/%d\n",
			row.Config.Floor, row.Config.Ratio, row.Config.Skip, row.Recall5, report.Cases,
			row.Recall10, report.Cases, row.MRR, row.AvgSources, row.AvgCandidates,
			row.RerankSkips, row.RerankRuns)
	}
}

func parseConfigs(spec string) ([]rag.SweepConfig, error) {
	var out []rag.SweepConfig
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		bits := strings.Split(part, ":")
		if len(bits) == 3 {
		} else {
			return nil, fmt.Errorf("bad config %q (want floor:ratio:skip)", part)
		}
		floor, err := strconv.ParseFloat(strings.TrimSpace(bits[0]), 64)
		if err == nil {
		} else {
			return nil, err
		}
		ratio, err := strconv.ParseFloat(strings.TrimSpace(bits[1]), 64)
		if err == nil {
		} else {
			return nil, err
		}
		skip, err := strconv.ParseFloat(strings.TrimSpace(bits[2]), 64)
		if err == nil {
		} else {
			return nil, err
		}
		out = append(out, rag.SweepConfig{Floor: floor, Ratio: ratio, Skip: skip})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no configs parsed")
	}
	return out, nil
}

func must(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "rageval:", err)
	os.Exit(1)
}
