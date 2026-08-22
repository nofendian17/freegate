package proxy

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestReproNormalStream(t *testing.T) {
	in := ""
	// A completely normal text stream: every delta has finish_reason:null.
	for _, txt := range []string{"Hello", " world", "!"} {
		in += fmt.Sprintf("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", txt)
	}
	in += "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	in += "data: [DONE]\n\n"
	var out bytes.Buffer
	normalizeOpenAIStream(&out, bufio.NewReader(strings.NewReader(in)))
	fmt.Println("--- NORMAL STREAM OUTPUT ---")
	fmt.Print(out.String())
	fmt.Println("--- END ---")
}
