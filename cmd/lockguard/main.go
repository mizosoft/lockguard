package main

import (
	"lockguard"

	"golang.org/x/tools/go/analysis/singlechecker"
)

func main() {
	singlechecker.Main(lockguard.Analyzer)
}
