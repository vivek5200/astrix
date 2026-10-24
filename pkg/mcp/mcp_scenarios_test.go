package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestProtocolAdversarialInputsReturnJSONRPCErrors(t *testing.T) {
	server := NewServer(map[string]Handler{"echo": func(_ context.Context, args map[string]any) (any, error) { return args, nil }})
	input := strings.Join([]string{
		`{not json`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"missing-tool"}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call"}`,
		`{"jsonrpc":"1.0","id":3,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":4,"method":"unknown/method"}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d responses, want 5: %s", len(lines), output.String())
	}
	wantCodes := []float64{-32700, -32602, -32602, -32600, -32601}
	for i, line := range lines {
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("response %d is invalid JSON: %v", i, err)
		}
		if response["jsonrpc"] != "2.0" {
			t.Errorf("response %d missing JSON-RPC 2.0 marker: %#v", i, response)
		}
		errObj, ok := response["error"].(map[string]any)
		if !ok || errObj["code"] != wantCodes[i] {
			t.Errorf("response %d error=%#v want code %v", i, response["error"], wantCodes[i])
		}
	}
}

func TestConcurrentServeCallsAreRaceSafe(t *testing.T) {
	var calls atomic.Int64
	server := NewServer(map[string]Handler{"count": func(context.Context, map[string]any) (any, error) {
		return calls.Add(1), nil
	}})
	const workers = 32
	serverInput, clientInput := io.Pipe()
	var output bytes.Buffer
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(context.Background(), serverInput, &output) }()
	var group sync.WaitGroup
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func(id int) {
			defer group.Done()
			request := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"count","arguments":{}}}`+"\n", id)
			if _, err := io.WriteString(clientInput, request); err != nil {
				errCh <- err
			}
		}(i)
	}
	group.Wait()
	if err := clientInput.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serveErr; err != nil {
		t.Fatal(err)
	}
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if got := calls.Load(); got != workers {
		t.Fatalf("handler calls=%d, want %d", got, workers)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != workers {
		t.Fatalf("got %d responses, want %d", len(lines), workers)
	}
	for i, line := range lines {
		var response map[string]any
		if err := json.Unmarshal([]byte(line), &response); err != nil {
			t.Fatalf("response %d invalid JSON: %v", i, err)
		}
		if response["error"] != nil || response["result"] == nil {
			t.Errorf("response %d invalid: %#v", i, response)
		}
	}
}
