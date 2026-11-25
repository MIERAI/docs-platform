package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/prometheus/client_golang/prometheus"
)

const tokenEnv = "TOOL_TOKEN"

var requests = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "tool", Name: "requests_total", Help: "x"})

func main() {
	verbose := flag.Bool("verbose", false, "verbose output")
	color := flag.String("color", "auto", "colour mode")
	port := flag.Int("port", 8080, "listen port")
	flag.Parse()
	_ = os.Getenv(tokenEnv)
	_ = os.Getenv("TOOL_UNDOCUMENTED")
	switch os.Args[1] {
	case "check":
		fmt.Println(*verbose, *color, *port)
	case "serve":
		requests.Inc()
	}
}
