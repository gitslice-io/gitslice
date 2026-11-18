package main

import (
	"flag"
	"log"
	"os"

	"gitslice.io/gitslice/server/internal/connectgen"
)

func main() {
	outPath := flag.String("out", "connect_adapters_gen.go", "output Go source path")
	flag.Parse()

	src, err := connectgen.Generate()
	if err != nil {
		log.Fatalf("connectgen: %v", err)
	}
	if err := os.WriteFile(*outPath, src, 0o644); err != nil {
		log.Fatalf("write %s: %v", *outPath, err)
	}
}
