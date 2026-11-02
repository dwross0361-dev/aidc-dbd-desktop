// Package console holds the Deployment Console page, served byte-identical to the Python service's copy.
package console

import _ "embed"

//go:embed console.html
var HTML []byte
