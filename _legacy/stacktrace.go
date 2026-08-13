package main

import (
	"fmt"
	"html"
	"net/http"
	"runtime"

	"github.com/pkg/errors"
)

// _printStackTrace prints a stack trace to the response in HTML format.
func _printStackTrace(w http.ResponseWriter, err error, sql string) {
	type stackTracer interface {
		StackTrace() []uintptr
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	w.WriteHeader(http.StatusBadRequest)

	var err2 stackTracer
	var res = `
<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <title>Error Stack Trace</title>
    <link rel="stylesheet" href="/css/pgrel.css">
</head>
<body>
    <div class="error-container">
        <div class="error-header">` + html.EscapeString(err.Error()) + `</div>`
	if sql != "" && DEBUG_ENABLED {
		res += `<pre class="error-sql">` + "\n" + html.EscapeString(sql) + "\n" + `</pre>`
	}
	res += `<table class="stack-table">
            <thead>
                <tr>
                    <th>#</th>
                    <th>File</th>
                    <th>Function</th>
                </tr>
            </thead>
            <tbody>`

	if errors.As(err, &err2) {
		frames := runtime.CallersFrames(err2.StackTrace())
		frameNum := 1
		for {
			if frame, ok := frames.Next(); !ok {
				break
			} else {
				res += fmt.Sprintf(`
                <tr>
                    <td>%d</td>
                    <td class="file-path">%s <span class="line-number">%d</span></td>
                    <td class="function-name">%s</td>
                </tr>`, frameNum, html.EscapeString(frame.File), frame.Line, html.EscapeString(frame.Function))
				frameNum++
			}
		}
	}

	res += `
            </tbody>
        </table>
    </div>
</body>
</html>`

	w.Write([]byte(res))
}
