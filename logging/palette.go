package logging

// The pretty handler's colors, as codes of the fixed 256-color palette: unlike the 16 basic
// colors, terminal themes don't remap them. Every code is readable on a black and on a white
// background, and the ones that can share a line are far apart from each other.
const (
	colorPath      = 30  // teal
	colorRole      = 169 // pink
	colorGET       = 104 // periwinkle blue
	colorPOST      = 202 // orange
	colorOtherVerb = 243 // grey
	colorStatus2xx = 40  // green
	colorStatus4xx = 100 // olive yellow
	colorStatus5xx = 124 // dark red
	colorLevelInfo = 93  // violet
	colorTargets   = 39  // azure

	// write=true/false reuse the 4xx/2xx colors : the meaning carries over.
	colorWriteTrue  = colorStatus4xx
	colorWriteFalse = colorStatus2xx
	colorLevelDebug = colorOtherVerb
	colorLevelWarn  = colorStatus4xx
	colorLevelError = colorStatus5xx
)

// verbColor is the color of an HTTP method : GET and POST have their own, any other shares one.
func verbColor(method string) int {
	switch method {
	case "GET":
		return colorGET
	case "POST":
		return colorPOST
	}
	return colorOtherVerb
}

// statusColor is the color of a status code (-1 for none) and whether it is bold.
func statusColor(status int) (color int, bold bool) {
	switch {
	case status >= 200 && status < 300:
		return colorStatus2xx, false
	case status >= 400 && status < 500:
		return colorStatus4xx, true
	case status >= 500 && status < 600:
		return colorStatus5xx, true
	}
	return -1, false
}
