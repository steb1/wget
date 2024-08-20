package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/time/rate"
)

func extractDomain(rawURL string) (string, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	return parsedURL.Hostname(), nil
}

// getRelativePath returns the relative path of a URL
func getRelativePath(rawURL string) (string, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}

	relPath := parsedURL.Path
	if relPath == "" || strings.HasSuffix(relPath, "/") {
		relPath += "index.html"
	}

	return strings.TrimPrefix(relPath, "/"), nil
}

func extractLinks(content []byte, contentType string) ([]string, error) {
	var links []string

	if strings.Contains(contentType, "text/html") {
		doc, err := html.Parse(strings.NewReader(string(content)))
		if err != nil {
			return nil, err
		}

		var traverse func(*html.Node)
		traverse = func(n *html.Node) {
			if n.Type == html.ElementNode {
				var attr string
				switch n.Data {
				case "a", "link":
					attr = "href"
				case "img", "script":
					attr = "src"
				case "style":
					// Extract URLs from inline CSS
					cssLinks := extractCSSLinks(n.FirstChild.Data)
					links = append(links, cssLinks...)
				}

				if attr != "" {
					for _, a := range n.Attr {
						if a.Key == attr {
							links = append(links, a.Val)
							break
						}
					}
				}
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				traverse(c)
			}
		}
		traverse(doc)

		// Extract URLs from <style> tags
		styleNodes := extractStyleNodes(doc)
		for _, styleNode := range styleNodes {
			cssLinks := extractCSSLinks(styleNode.FirstChild.Data)
			links = append(links, cssLinks...)
		}
	} else if strings.Contains(contentType, "text/css") {
		cssLinks := extractCSSLinks(string(content))
		links = append(links, cssLinks...)
	}

	return links, nil
}

func extractStyleNodes(n *html.Node) []*html.Node {
	var styleNodes []*html.Node

	var traverse func(*html.Node)
	traverse = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "style" {
			styleNodes = append(styleNodes, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			traverse(c)
		}
	}
	traverse(n)

	return styleNodes
}

func extractCSSLinks(css string) []string {
	var links []string

	// Extract URLs from url() functions
	urlRegex := regexp.MustCompile(`url\(['"]?(.+?)['"]?\)`)
	matches := urlRegex.FindAllStringSubmatch(css, -1)
	for _, match := range matches {
		if len(match) > 1 {
			links = append(links, match[1])
		}
	}

	// Extract URLs from @import statements
	importRegex := regexp.MustCompile(`@import\s+(['"](.+?)['"]|url\(['"]?(.+?)['"]?\))`)
	importMatches := importRegex.FindAllStringSubmatch(css, -1)
	for _, match := range importMatches {
		if len(match) > 2 {
			if match[2] != "" {
				links = append(links, match[2])
			} else if match[3] != "" {
				links = append(links, match[3])
			}
		}
	}

	return links
}

// resolveURL resolves a potentially relative URL to an absolute URL
func resolveURL(base, ref string) (string, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", err
	}

	refURL, err := url.Parse(ref)
	if err != nil {
		return "", err
	}

	resolvedURL := baseURL.ResolveReference(refURL)
	return resolvedURL.String(), nil
}

// isSameDomain checks if two URLs belong to the same domain
func isSameDomain(url1, url2 string) bool {
	domain1, err1 := extractDomain(url1)
	domain2, err2 := extractDomain(url2)

	if err1 != nil || err2 != nil {
		return false
	}

	return domain1 == domain2
}

// sanitizeFilename removes or replaces characters that are not allowed in filenames
func sanitizeFilename(filename string) string {
	// Replace characters that are not allowed in filenames
	replacer := strings.NewReplacer(
		"<", "_",
		">", "_",
		":", "_",
		"\"", "_",
		"/", "_",
		"\\", "_",
		"|", "_",
		"?", "_",
		"*", "_",
	)

	sanitized := replacer.Replace(filename)
	return path.Clean(sanitized)
}

func parseFlags() Config {
	config := Config{}

	flag.Func("reject", "Reject file suffixes (comma-separated)", func(s string) error {
		config.RejectSuffixes = strings.Split(s, ",")
		return nil
	})
	flag.Func("X", "Exclude paths (comma-separated)", func(s string) error {
		config.ExcludePaths = strings.Split(s, ",")
		return nil
	})
	flag.BoolVar(&config.ConvertLinks, "convert-links", false, "Convert links for offline viewing")

	flag.BoolVar(&config.Background, "B", false, "Download in background")
	flag.StringVar(&config.OutputName, "O", "", "Save file under a different name")
	flag.StringVar(&config.OutputPath, "P", "", "Path to save the file")
	flag.StringVar(&config.RateLimit, "rate-limit", "", "Limit download speed (e.g., 200k, 2M)")
	flag.StringVar(&config.InputFile, "i", "", "File containing URLs to download")
	flag.BoolVar(&config.Mirror, "mirror", false, "Mirror a website")

	flag.Parse()

	if flag.NArg() > 0 {
		config.URL = flag.Arg(0)
	}

	return config
}

func parseRateLimit(rateLimit string) (int64, error) {
	rateLimit = strings.TrimSpace(strings.ToLower(rateLimit))
	if rateLimit == "" {
		return 0, nil
	}

	var multiplier int64 = 1
	var value float64

	if strings.HasSuffix(rateLimit, "k") {
		multiplier = 1024
		rateLimit = strings.TrimSuffix(rateLimit, "k")
	} else if strings.HasSuffix(rateLimit, "m") {
		multiplier = 4 * 1024 * 1024
		rateLimit = strings.TrimSuffix(rateLimit, "m")
	}

	value, err := strconv.ParseFloat(rateLimit, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid rate limit format: %s", rateLimit)
	}

	return int64(value * float64(multiplier)), nil
}

func Download(config Config) error {
	startTime := time.Now()
	fmt.Printf("start at %s\n", startTime.Format("2006-01-02 15:04:05"))

	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: 30 * time.Minute,
	}

	// Send GET request
	req, err := http.NewRequest("GET", config.URL, nil)
	if err != nil {
		return fmt.Errorf("error creating request: %v", err)
	}

	// Add headers to mimic a browser request
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	fmt.Println("sending request, awaiting response...")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error sending request: %v", err)
	}
	defer resp.Body.Close()

	fmt.Printf("status %s\n", resp.Status)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	contentLength := resp.ContentLength
	fmt.Printf("content size: %d [~%.2fMB]\n", contentLength, float64(contentLength)/(1024*1024))

	// Determine output file name and path
	outputName := config.OutputName
	if outputName == "" {
		outputName = filepath.Base(config.URL)
	}
	outputPath := config.OutputPath
	if outputPath == "" {
		outputPath = "."
	}
	fullPath := filepath.Join(outputPath, outputName)

	// Create the directory if it doesn't exist
	err = os.MkdirAll(filepath.Dir(fullPath), 0755)
	if err != nil {
		return fmt.Errorf("error creating directory: %v", err)
	}

	fmt.Printf("saving file to: ./%s\n", fullPath)

	// Create output file
	file, err := os.Create(fullPath)
	if err != nil {
		return fmt.Errorf("error creating file: %v", err)
	}
	defer file.Close()

	// Create progress bar
	progressBar := &ProgressBar{startTime: time.Now()}

	// Apply rate limiting if specified
	reader, err := limitRate(resp.Body, config.RateLimit)
	if err != nil {
		return fmt.Errorf("error applying rate limit: %v", err)
	}

	// Create a buffer for copying
	buf := make([]byte, 1024*1024)
	totalRead := int64(0)
	lastUpdate := time.Now()

	for {
		n, err := reader.Read(buf)
		if n > 0 {
			totalRead += int64(n)
			_, writeErr := file.Write(buf[:n])
			if writeErr != nil {
				return fmt.Errorf("error writing to file: %v", writeErr)
			}

			// Update progress bar
			if time.Since(lastUpdate) >= time.Second {
				progressBar.Update(totalRead, contentLength)
				lastUpdate = time.Now()
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error reading response body: %v", err)
		}
	}

	progressBar.Update(totalRead, contentLength)

	finishTime := time.Now()
	fmt.Printf("\nDownloaded [%s]\n", config.URL)
	fmt.Printf("finished at %s\n", finishTime.Format("2006-01-02 15:04:05"))
	fmt.Printf("Total time: %v\n", finishTime.Sub(startTime))

	return nil
}

func downloadInBack(config Config) error {
	// Disable timestamp in logs
	log.SetFlags(0)
	startTime := time.Now()
	log.Printf("start at %s\n", startTime.Format("2006-01-02 15:04:05"))

	// Create HTTP client with timeout
	client := &http.Client{
		Timeout: 30 * time.Minute,
	}

	// Send GET request
	req, err := http.NewRequest("GET", config.URL, nil)
	if err != nil {
		return fmt.Errorf("error creating request: %v", err)
	}

	// Add headers to mimic a browser request
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")

	log.Println("sending request, awaiting response...")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("error sending request: %v", err)
	}
	defer resp.Body.Close()

	log.Printf("status %s\n", resp.Status)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	contentLength := resp.ContentLength
	//log.Printf("content size: %d [~%.2fMB]\n", contentLength, float64(contentLength)/(1024*1024))

	// Determine output file name and path
	outputName := config.OutputName
	if outputName == "" {
		outputName = filepath.Base(config.URL)
	}
	outputPath := config.OutputPath
	if outputPath == "" {
		outputPath = "."
	}
	fullPath := filepath.Join(outputPath, outputName)
	log.Printf("saving file to: ./%s\n", fullPath)

	// Create the directory if it doesn't exist
	err = os.MkdirAll(filepath.Dir(fullPath), 0755)
	if err != nil {
		return fmt.Errorf("error creating directory: %v", err)
	}

	// Create output file
	file, err := os.Create(fullPath)
	if err != nil {
		return fmt.Errorf("error creating file: %v", err)
	}
	defer file.Close()

	// Create progress bar
	progressBar := createProgressBar(contentLength)

	// Apply rate limiting if specified
	reader, err := limitRate(resp.Body, config.RateLimit)
	if err != nil {
		return fmt.Errorf("error applying rate limit: %v", err)
	}

	// Create a buffer for copying
	buf := make([]byte, 1024*1024)
	totalRead := int64(0)
	lastUpdate := time.Now()

	for {
		n, err := reader.Read(buf)
		if n > 0 {
			totalRead += int64(n)
			_, writeErr := file.Write(buf[:n])
			if writeErr != nil {
				return fmt.Errorf("error writing to file: %v", writeErr)
			}

			//Update progress bar
			progressBar.Update(totalRead, contentLength)

			//Calculate and display download speed
			if time.Since(lastUpdate) >= time.Second {
				//speed := float64(totalRead) / time.Since(startTime).Seconds() / 1024 // KB/s
				//remainingTime := time.Duration(float64(contentLength-totalRead)/speed/1024) * time.Second
				//log.Printf("\rProgress: %.2f%% | Speed: %.2f KB/s | ETA: %v", float64(totalRead)/float64(contentLength)*100, speed, remainingTime)
				lastUpdate = time.Now()
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error reading response body: %v", err)
		}
	}

	progressBar.Finish()

	finishTime := time.Now()
	log.Printf("\nDownloaded [%s]\n", config.URL)
	log.Printf("finished at %s\n", finishTime.Format("2006-01-02 15:04:05"))
	//log.Printf("Total time: %v\n", finishTime.Sub(startTime))

	return nil
}

func createProgressBar(total int64) *ProgressBar {
	return &ProgressBar{startTime: time.Now()}
}

type ProgressBar struct {
	startTime time.Time
}

func (pb *ProgressBar) Update(current, total int64) {
	if total <= 0 {
		return
	}
	percentage := float64(current) / float64(total) * 100
	width := 50
	completed := int(percentage / 100 * float64(width))

	elapsed := time.Since(pb.startTime)
	speed := float64(current) / elapsed.Seconds() / 1024 // KB/s
	remaining := time.Duration(float64(total-current)/speed/1024) * time.Second

	fmt.Printf("\r[%-50s] %.2f%% | %.2f KB/s | ETA: %v",
		strings.Repeat("=", completed)+strings.Repeat(" ", width-completed),
		percentage,
		speed,
		remaining.Round(time.Second))
}

func (pb *ProgressBar) Finish() {
	fmt.Println()
}

func downloadInBackground(config Config) {
	fmt.Println("Output will be written to \"wget-log.txt\"")
	logFile, err := os.Create("wget-log.txt")
	if err != nil {
		fmt.Printf("Error creating log file: %v\n", err)
		return
	}
	defer logFile.Close()

	// Redirect stdout and stderr to the log file
	log.SetOutput(logFile)

	// Create a channel to wait for the download to complete
	done := make(chan bool)

	// Run the download in a goroutine
	go func() {
		log.Println("Downloading in background...")
		err := downloadInBack(config)
		if err != nil {
			log.Printf("Error: %v\n", err)
		}
		done <- true
	}()

	// Wait for the download to complete
	<-done

	// Exit the main process after the download completes
	os.Exit(0)
}

func downloadMultipleFiles(config Config) error {
	urls, err := readURLsFromFile(config.InputFile)
	if err != nil {
		return fmt.Errorf("error reading input file: %v", err)
	}

	fmt.Printf("Starting concurrent download of %d files\n", len(urls))

	var wg sync.WaitGroup
	results := make(chan string, len(urls))

	for _, url := range urls {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			err := downloadFile(url, config)
			if err != nil {
				results <- fmt.Sprintf("Error downloading %s: %v", url, err)
			} else {
				results <- fmt.Sprintf("Successfully downloaded %s", url)
			}
		}(url)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	for result := range results {
		fmt.Println(result)
	}

	fmt.Println("All downloads completed")
	return nil
}

func readURLsFromFile(filename string) ([]string, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var urls []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		url := scanner.Text()
		if url != "" {
			urls = append(urls, url)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return urls, nil
}

func downloadFile(url string, config Config) error {
	// Create a new config for this download
	downloadConfig := config
	downloadConfig.URL = url

	// Use a temporary file name based on the URL
	downloadConfig.OutputName = fmt.Sprintf("%d_%s", time.Now().UnixNano(), filepath.Base(url))

	err := Download(downloadConfig)
	if err != nil {
		return err
	}

	return nil
}

func shouldReject(url string, rejectSuffixes []string) bool {
	for _, suffix := range rejectSuffixes {
		if strings.HasSuffix(strings.ToLower(url), strings.ToLower(suffix)) {
			return true
		}
	}
	return false
}

func shouldExclude(url string, excludePaths []string) bool {
	for _, path := range excludePaths {
		if strings.Contains(url, path) {
			return true
		}
	}
	return false
}

func convertLinks(outputDir string) error {
	return filepath.Walk(outputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && (strings.HasSuffix(strings.ToLower(path), ".html") || strings.HasSuffix(strings.ToLower(path), ".css")) {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			converted := convertURLsInContent(string(content), path, outputDir)

			err = os.WriteFile(path, []byte(converted), 0644)
			if err != nil {
				return err
			}
		}

		return nil
	})
}

func convertURLsInContent(content, filePath, outputDir string) string {
	isHTML := strings.HasSuffix(strings.ToLower(filePath), ".html")
	isCSS := strings.HasSuffix(strings.ToLower(filePath), ".css")

	convertURL := func(originalURL string) string {
		if strings.HasPrefix(originalURL, "http://") || strings.HasPrefix(originalURL, "https://") {
			// External URL, don't modify
			return originalURL
		}

		// Remove leading '/' if present
		if strings.HasPrefix(originalURL, "/") {
			originalURL = originalURL[1:]
		}

		// Construct the path relative to the current file
		relativePath := filepath.Join(filepath.Dir(filePath), originalURL)
		newPath, err := filepath.Rel(filepath.Dir(filePath), relativePath)
		if err != nil {
			// If we can't make it relative, use the original URL
			return originalURL
		}

		// Convert Windows path separators to forward slashes for URLs
		return strings.ReplaceAll(newPath, "\\", "/")
	}

	if isHTML {
		doc, err := html.Parse(strings.NewReader(content))
		if err != nil {
			return content
		}

		var traverse func(*html.Node)
		traverse = func(n *html.Node) {
			if n.Type == html.ElementNode {
				for i, a := range n.Attr {
					if (n.Data == "a" && a.Key == "href") ||
						((n.Data == "img" || n.Data == "script") && a.Key == "src") ||
						(n.Data == "link" && a.Key == "href") {
						n.Attr[i].Val = convertURL(a.Val)
					} else if n.Data == "style" {
						// Handle inline styles
						n.Attr[i].Val = convertCSSURLs(a.Val, convertURL)
					}
				}
			} else if n.Type == html.TextNode && n.Parent != nil && n.Parent.Data == "style" {
				// Handle <style> tag contents
				n.Data = convertCSSURLs(n.Data, convertURL)
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				traverse(c)
			}
		}
		traverse(doc)

		var b strings.Builder
		err = html.Render(&b, doc)
		if err != nil {
			return content
		}
		return b.String()
	} else if isCSS {
		return convertCSSURLs(content, convertURL)
	}

	return content
}

func convertCSSURLs(css string, convertURL func(string) string) string {
	urlRegex := regexp.MustCompile(`url\(['"]?(.+?)['"]?\)`)
	return urlRegex.ReplaceAllStringFunc(css, func(match string) string {
		submatches := urlRegex.FindStringSubmatch(match)
		if len(submatches) < 2 {
			return match
		}
		newURL := convertURL(submatches[1])
		return strings.Replace(match, submatches[1], newURL, 1)
	})
}

func limitRate(reader io.Reader, rateLimit string) (io.Reader, error) {
	limit, err := parseRateLimit(rateLimit)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		return reader, nil
	}

	limiter := rate.NewLimiter(rate.Limit(limit), int(limit)/10) // burst de 1/10 de seconde

	return &RateLimitedReader{
		reader:    reader,
		rateLimit: int64(limiter.Limit()),
		lastRead:  time.Now(),
	}, nil
}

type RateLimitedReader struct {
	reader    io.Reader
	rateLimit int64
	lastRead  time.Time
	bytesRead int64
	startTime time.Time
}

func (r *RateLimitedReader) Read(p []byte) (int, error) {
    if r.startTime.IsZero() {
        r.startTime = time.Now() // Initialize the start time on the first read
    }

    // Calculate the maximum number of bytes that can be read without exceeding the rate limit
    elapsedTime := time.Since(r.startTime).Seconds()
    allowedBytes := int64(elapsedTime * float64(r.rateLimit))

    // If we've already read more than the allowed bytes, wait until we're allowed to read more
    if r.bytesRead >= allowedBytes {
        sleepDuration := time.Duration(float64(r.bytesRead-allowedBytes)/float64(r.rateLimit)) * time.Second
        time.Sleep(sleepDuration)

        // Recalculate allowed bytes after sleeping
        elapsedTime = time.Since(r.startTime).Seconds()
        allowedBytes = int64(elapsedTime * float64(r.rateLimit))
    }

    // Calculate the maximum number of bytes to read this time to stay within the limit
    remainingBytes := allowedBytes - r.bytesRead
    if remainingBytes + 100 < int64(len(p)) {
        p = p[:remainingBytes] // Adjust the buffer size to not exceed the allowed bytes
    }

    // Read the data
    n, err := r.reader.Read(p)
    r.bytesRead += int64(n)

    return n, err
}
