package util

import (
	"bufio"
	"fmt"
	"io"
)

type OutputWriter struct {
	io.Writer
}

var _ io.Writer = (*OutputWriter)(nil)

func NewOutputWriter(prefix string, output io.Writer) *OutputWriter {
	r, w := io.Pipe()
	br := bufio.NewReader(r)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				_, _ = fmt.Fprintf(output, "%s: %s", prefix, line)
			}
			if err != nil {
				break
			}
		}
	}()
	return &OutputWriter{
		Writer: w,
	}
}
