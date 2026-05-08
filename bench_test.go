package main

import (
	"fmt"
	"os"
	"testing"
)

// Run with: go test -bench=BenchmarkScan -benchtime=1x -run=^$ ./...
// Set TIDY_BENCH_ROOT to point at a real tree, e.g. ~/code.
func BenchmarkScan(b *testing.B) {
	root := benchRoot()
	if root == "" {
		b.Skip("set TIDY_BENCH_ROOT to a directory to benchmark")
	}
	for i := 0; i < b.N; i++ {
		items, err := Scan(root)
		if err != nil {
			b.Fatal(err)
		}
		var total int64
		for _, it := range items {
			total += it.Size
		}
		b.ReportMetric(float64(len(items)), "artifacts")
		b.ReportMetric(float64(total)/(1024*1024*1024), "GiB")
		fmt.Println("found:", len(items), "total bytes:", total)
	}
}

func benchRoot() string {
	return os.Getenv("TIDY_BENCH_ROOT")
}
