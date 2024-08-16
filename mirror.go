package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func mirrorPage(config Config, pageURL, outputDir string, visited *SafeMap, wg *sync.WaitGroup, semaphore chan struct{}) error {
	if visited, _ := visited.Get(pageURL); visited {
		return nil
	}
	visited.Set(pageURL, true)

	if shouldReject(pageURL, config.RejectSuffixes) {
		return nil
	}

	if shouldExclude(pageURL, config.ExcludePaths) {
		return nil
	}

	semaphore <- struct{}{}
	defer func() { <-semaphore }()

	resp, err := http.Get(pageURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d for %s", resp.StatusCode, pageURL)
	}

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	relativePath, err := getRelativePath(pageURL)
	if err != nil {
		return err
	}

	outputPath := filepath.Join(outputDir, relativePath)
	err = os.MkdirAll(filepath.Dir(outputPath), 0755)
	if err != nil {
		return err
	}

	err = os.WriteFile(outputPath, content, 0644)
	if err != nil {
		return err
	}

	fmt.Printf("Downloaded: %s\n", pageURL)

	// Parse HTML and CSS files for links
	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/html") || strings.Contains(contentType, "text/css") {
		links, err := extractLinks(content, contentType)
		if err != nil {
			return err
		}

		for _, link := range links {
			absoluteURL, err := resolveURL(pageURL, link)
			if err != nil {
				fmt.Printf("Error resolving URL %s: %v\n", link, err)
				continue
			}

			if isSameDomain(pageURL, absoluteURL) {
				wg.Add(1)
				go func(u string) {
					defer wg.Done()
					err := mirrorPage(config, u, outputDir, visited, wg, semaphore)
					if err != nil {
						fmt.Printf("Error mirroring page %s: %v\n", u, err)
					}
				}(absoluteURL)
			}
		}
	}

	return nil
}

func mirrorWebsite(config Config) error {
	parsedURL, err := url.Parse(config.URL)
	if err != nil {
		return fmt.Errorf("error parsing URL: %v", err)
	}

	domain := parsedURL.Hostname()
	outputDir := filepath.Join(config.OutputPath, domain)
	err = os.MkdirAll(outputDir, 0755)
	if err != nil {
		return fmt.Errorf("error creating output directory: %v", err)
	}

	visited := &SafeMap{m: make(map[string]bool)}
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 5) // Limit concurrent downloads

	err = mirrorPage(config, config.URL, outputDir, visited, &wg, semaphore)
	if err != nil {
		return fmt.Errorf("error mirroring website: %v", err)
	}

	wg.Wait()

	if config.ConvertLinks {
		err = convertLinks(outputDir)
		if err != nil {
			return fmt.Errorf("error converting links: %v", err)
		}
	}

	return nil
}
