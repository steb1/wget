package main

import (
	"fmt"
	"os"
	"sync"
)

type Config struct {
	URL            string
	Background     bool
	OutputName     string
	OutputPath     string
	RateLimit      string
	InputFile      string
	Mirror         bool
	RejectSuffixes []string
	ExcludePaths   []string
	ConvertLinks   bool
}

type SafeMap struct {
	m   map[string]bool
	mux sync.Mutex
}

func (sm *SafeMap) Set(key string, value bool) {
	sm.mux.Lock()
	defer sm.mux.Unlock()
	sm.m[key] = value
}

func (sm *SafeMap) Get(key string) (bool, bool) {
	sm.mux.Lock()
	defer sm.mux.Unlock()
	val, ok := sm.m[key]
	return val, ok
}

func main() {
	config := parseFlags()

	if config.Background {
		downloadInBackground(config)
	} else if config.InputFile != "" {
		downloadMultipleFiles(config)
	} else if config.Mirror {
		mirrorWebsite(config)
	} else {
		err := Download(config)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
	}
}