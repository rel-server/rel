package main

import (
	"bytes"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"

	"github.com/fatih/color"
)

// Recoverer is a middleware that recovers from panics and logs a colored stack trace
func RecovererColored(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				stack := filterStack(debug.Stack())

				red := color.New(color.FgHiRed, color.Bold).SprintFunc()
				yellow := color.New(color.FgYellow).SprintFunc()
				cyan := color.New(color.FgCyan).SprintFunc()

				fmt.Println(red("=== PANIC RECOVERED ==="))
				fmt.Printf("%s: %v\n", yellow("Recovered at"), r.URL.Path)
				fmt.Println()
				fmt.Println(cyan("Stack Trace:"))
				fmt.Println(stack)
				fmt.Println(red("======================="))

				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}()

		next.ServeHTTP(w, r)
	})
}

// filterStack trims irrelevant lines (net/http, chi) and highlights the first non-framework caller
func filterStack(stack []byte) string {
	lines := bytes.Split(stack, []byte{'\n'})
	var out []string
	highlighted := false

	for i := 0; i < len(lines)-1; i += 2 {
		fnLine := string(lines[i])
		locLine := string(lines[i+1])

		// Skip chi, net/http, runtime, or testing
		// if strings.Contains(fnLine, "net/http") ||
		// 	strings.Contains(fnLine, "chi/") ||
		// 	strings.Contains(fnLine, "runtime/") ||
		// 	strings.Contains(fnLine, "testing/") {
		// 	continue
		// }

		// Highlight first relevant line
		if !highlighted {
			fnLine = color.New(color.Bold, color.FgRed).Sprint(fnLine)
			locLine = color.New(color.Bold, color.FgWhite).Sprint(locLine)
			highlighted = true
		} else {
			fnLine = color.New(color.FgCyan).Sprint(fnLine)
			locLine = color.New(color.FgHiBlack).Sprint(locLine)
		}

		out = append(out, fnLine)
		out = append(out, locLine)
	}
	return strings.Join(out, "\n")
}
