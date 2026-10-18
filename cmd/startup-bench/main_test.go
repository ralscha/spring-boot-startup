package main

import (
	"strings"
	"testing"
)

func TestNativeCodeComparison(t *testing.T) {
	control := benchmarkResult{Variant: "aot-cache-java28-no-aot-code", RunsRequested: 15, SuccessfulRuns: 15, Summary: summary{MedianMS: 2000}}
	candidate := benchmarkResult{Variant: "aot-cache-java28", RunsRequested: 15, SuccessfulRuns: 15, Summary: summary{MedianMS: 1500}}
	got := renderMarkdown([]benchmarkResult{control, candidate})
	if !strings.Contains(got, "2000.00 ms → 1500.00 ms (-25.00% median time") {
		t.Fatalf("missing measured native-code comparison:\n%s", got)
	}
	for _, incomplete := range []benchmarkResult{
		{Variant: candidate.Variant, RunsRequested: 15, SuccessfulRuns: 14, Summary: candidate.Summary},
		{Variant: candidate.Variant, RunsRequested: 15, SuccessfulRuns: 15, FailureMessage: "failed", Summary: candidate.Summary},
		{Variant: candidate.Variant},
	} {
		if got := renderMarkdown([]benchmarkResult{control, incomplete}); strings.Contains(got, "JEP 544:") {
			t.Fatalf("reported an improvement from incomplete results:\n%s", got)
		}
	}
}
