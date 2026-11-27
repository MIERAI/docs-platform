// Command server is a fixture of the rules stage: flags, environment variables, constants and
// routes whose documentation is partly right and partly wrong.
package main

import (
	"cmp"
	"flag"
	"net/http"
	"os"
	"time"

	"example.com/fixture/internal/config"
)

// DefaultPort is the port the server listens on.
const DefaultPort = 8080

// MaxBodyBytes bounds a request body.
const MaxBodyBytes = 1 << 20

func main() {
	port := flag.Int("port", DefaultPort, "listen port")
	workers := flag.Int("workers", 8, "number of worker goroutines")
	timeout := flag.Duration("timeout", 30*time.Second, "request timeout")
	format := flag.String("log-format", "json", "log format")
	queue := flag.Int("queue", 0, "queue size (default 256)")
	flag.Parse()
	region := cmp.Or(os.Getenv("SERVER_REGION"), "eu-west-1")
	token := os.Getenv("SERVER_TOKEN")
	_ = config.New()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/orders/{id}", getOrder)
	mux.HandleFunc("POST /v1/orders", createOrder)
	mux.HandleFunc("/healthz", health)
	_, _, _, _, _, _, _ = port, workers, timeout, format, queue, region, token
}

func getOrder(http.ResponseWriter, *http.Request)    {}
func createOrder(http.ResponseWriter, *http.Request) {}
func health(http.ResponseWriter, *http.Request)      {}
