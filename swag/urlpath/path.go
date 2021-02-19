package urlpath

import (
	stdpath "path"
	"strings"
)

func Clean(path string) string {
	path = stdpath.Clean("/" + path)
	if path[len(path)-1] != '/' {
		path = path + "/"
	}
	return path
}

func Split(path string) (head, tail string) {
	path = Clean(path)
	parts := strings.SplitN(path[1:], "/", 2)
	if len(parts) < 2 {
		parts = append(parts, "/")
	}
	return parts[0], Clean(parts[1])
}

