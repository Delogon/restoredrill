package main

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// probeCmd is hidden: it runs inside the probe container on the isolated
// network so HTTP checks need no curl image.
var probeCmd = &cobra.Command{
	Use:    "probe",
	Short:  "Internal probe helpers (run inside the probe container)",
	Hidden: true,
}

var (
	probeURL     string
	probeMethod  string
	probeHeaders []string
	probeTimeout time.Duration
)

var probeHTTPCmd = &cobra.Command{
	Use:   "http",
	Short: "Issue an HTTP request and print a JSON result",
	RunE: func(cmd *cobra.Command, args []string) error {
		result := map[string]any{}
		start := time.Now()
		method := probeMethod
		if method == "" {
			method = http.MethodGet
		}
		req, err := http.NewRequest(method, probeURL, nil)
		if err != nil {
			result["error"] = err.Error()
			return printProbeResult(result)
		}
		for _, h := range probeHeaders {
			k, v, ok := strings.Cut(h, ":")
			if ok {
				req.Header.Set(strings.TrimSpace(k), strings.TrimSpace(v))
			}
		}
		client := &http.Client{Timeout: probeTimeout}
		resp, err := client.Do(req)
		if err != nil {
			result["error"] = err.Error()
			return printProbeResult(result)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			result["error"] = err.Error()
			return printProbeResult(result)
		}
		result["status"] = resp.StatusCode
		result["body"] = string(body)
		result["duration_ms"] = time.Since(start).Milliseconds()
		return printProbeResult(result)
	},
}

var probeWaitCmd = &cobra.Command{
	Use:   "wait",
	Short: "Block until signalled (keeps the probe container alive)",
	RunE: func(cmd *cobra.Command, args []string) error {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		return nil
	},
}

func printProbeResult(result map[string]any) error {
	enc := json.NewEncoder(os.Stdout)
	return enc.Encode(result)
}

func init() {
	probeHTTPCmd.Flags().StringVar(&probeURL, "url", "", "URL to probe")
	probeHTTPCmd.Flags().StringVar(&probeMethod, "method", "GET", "HTTP method")
	probeHTTPCmd.Flags().StringArrayVar(&probeHeaders, "header", nil, "header K:V (repeatable)")
	probeHTTPCmd.Flags().DurationVar(&probeTimeout, "timeout", 30*time.Second, "request timeout")
	probeCmd.AddCommand(probeHTTPCmd)
	probeCmd.AddCommand(probeWaitCmd)
	rootCmd.AddCommand(probeCmd)
}