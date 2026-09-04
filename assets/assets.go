package assets

import "embed"

//go:embed lima.yaml
var LimaTemplate []byte

//go:embed mcp
var mcp embed.FS

func MCP() map[string][]byte {
	out := map[string][]byte{}
	entries, err := mcp.ReadDir("mcp")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if body, err := mcp.ReadFile("mcp/" + e.Name()); err == nil {
			out[e.Name()] = body
		}
	}
	return out
}
