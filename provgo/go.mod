module aidc-dbd/provgo

go 1.24

replace golang.org/x/sys => github.com/golang/sys v0.28.0

replace golang.org/x/text => github.com/golang/text v0.21.0

require (
	github.com/ncruces/go-sqlite3 v0.25.2
	golang.org/x/crypto v0.38.0
)

require (
	github.com/ncruces/julianday v1.0.0 // indirect
	github.com/tetratelabs/wazero v1.9.0 // indirect
	golang.org/x/sys v0.33.0 // indirect
)

replace golang.org/x/crypto => github.com/golang/crypto v0.31.0
