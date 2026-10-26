package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

func TestServeFlushesResponseBeforeInputEOF(t *testing.T) {
	server := NewServer(nil)
	serverInput, clientInput := io.Pipe()
	clientOutput, serverOutput := io.Pipe()
	t.Cleanup(func() {
		_ = clientInput.Close()
		_ = clientOutput.Close()
	})
	serveDone := make(chan error, 1)
	go func() {
		err := server.Serve(context.Background(), serverInput, serverOutput)
		_ = serverOutput.Close()
		serveDone <- err
	}()

	if _, err := io.WriteString(clientInput, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n"); err != nil {
		t.Fatal(err)
	}

	response := make(chan string, 1)
	readErr := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(clientOutput).ReadString('\n')
		if err != nil {
			readErr <- err
			return
		}
		response <- line
	}()

	var line string
	select {
	case line = <-response:
	case err := <-readErr:
		t.Fatalf("read MCP response: %v", err)
	case <-time.After(time.Second):
		t.Fatal("server did not flush its response before input EOF")
	}

	if err := clientInput.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("serve: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("invalid JSON-RPC response %q: %v", line, err)
	}
	if got["id"] != float64(1) {
		t.Fatalf("response id=%v, want 1", got["id"])
	}
}

func TestServeHandlesInitializeToolCallAndNotification(t *testing.T) {
	server := NewServer(map[string]Handler{"astrix_get_blast_radius": func(_ context.Context, args map[string]any) (any, error) {
		return map[string]any{"count": args["count"]}, nil
	}})
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n" + `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" + `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"astrix_get_blast_radius","arguments":{"count":2}}}` + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("responses=%d: %s", len(lines), output.String())
	}
	var init map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &init); err != nil {
		t.Fatal(err)
	}
	if init["id"].(float64) != 1 {
		t.Fatalf("initialize response=%v", init)
	}
	var call map[string]any
	if err := json.Unmarshal([]byte(lines[2]), &call); err != nil {
		t.Fatal(err)
	}
	if call["id"].(float64) != 3 {
		t.Fatalf("tool response=%v", call)
	}
}

func TestServeMarksToolErrors(t *testing.T) {
	s := NewServer(map[string]Handler{"bad": func(context.Context, map[string]any) (any, error) { return nil, context.Canceled }})
	var out bytes.Buffer
	_ = s.Serve(context.Background(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"bad"}}`+"\n"), &out)
	if !strings.Contains(out.String(), `"isError":true`) {
		t.Fatalf("response=%s", out.String())
	}
}
