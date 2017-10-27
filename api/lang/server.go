// Copyright 2015 ThoughtWorks, Inc.

// This file is part of Gauge.

// Gauge is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

// Gauge is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.

// You should have received a copy of the GNU General Public License
// along with Gauge.  If not, see <http://www.gnu.org/licenses/>.

package lang

import (
	"context"
	"log"

	"os"

	"errors"

	"encoding/json"

	"github.com/getgauge/gauge/gauge"
	gm "github.com/getgauge/gauge/gauge_messages"
	"github.com/getgauge/gauge/logger"
	"github.com/getgauge/gauge/parser"
	"github.com/getgauge/gauge/runner"
	"github.com/getgauge/gauge/util"
	"github.com/sourcegraph/go-langserver/pkg/lsp"
	"github.com/sourcegraph/jsonrpc2"
)

type server struct{}

type infoProvider interface {
	Init()
	Steps() []*gauge.StepValue
	Concepts() []*gm.ConceptInfo
	Params(file string, argType gauge.ArgType) []gauge.StepArg
	SearchConceptDictionary(string) *gauge.Concept
	GetConceptDictionary() *gauge.ConceptDictionary
	UpdateConceptCache(string, string) *parser.ParseResult
}

var provider infoProvider

type langRunner struct {
	runner   runner.Runner
	killChan chan bool
}

var lRunner langRunner

func Server(p infoProvider) *server {
	provider = p
	provider.Init()
	lRunner.killChan = make(chan bool)
	var err error
	lRunner.runner, err = connectToRunner(lRunner.killChan)
	if err != nil {
		logger.APILog.Infof("Unable to connect to runner : %s", err.Error())
	}
	return &server{}
}

type lspHandler struct {
	jsonrpc2.Handler
}

type LangHandler struct {
}

type registrationParams struct {
	Registrations []registration `json:"registrations"`
}

type registration struct {
	Id              string          `json:"id"`
	Method          string          `json:"method"`
	RegisterOptions registerOptions `json:"registerOptions"`
}

type registerOptions struct {
	DocumentSelector documentSelector         `json:"documentSelector"`
	SyncKind         lsp.TextDocumentSyncKind `json:"syncKind,omitempty"`
}

type documentSelector struct {
	Language string `json:"language"`
}

func newHandler() jsonrpc2.Handler {
	return lspHandler{jsonrpc2.HandlerWithError((&LangHandler{}).handle)}
}

func (h lspHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	go h.Handler.Handle(ctx, conn, req)
}

func (h *LangHandler) handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) (interface{}, error) {
	return h.Handle(ctx, conn, req)
}

func (h *LangHandler) Handle(ctx context.Context, conn jsonrpc2.JSONRPC2, req *jsonrpc2.Request) (interface{}, error) {
	switch req.Method {
	case "initialize":
		kind := lsp.TDSKFull
		return lsp.InitializeResult{
			Capabilities: lsp.ServerCapabilities{
				TextDocumentSync:           lsp.TextDocumentSyncOptionsOrKind{Kind: &kind},
				CompletionProvider:         &lsp.CompletionOptions{ResolveProvider: true, TriggerCharacters: []string{"*", "* ", "\"", "<"}},
				DocumentFormattingProvider: true,
				CodeLensProvider:           &lsp.CodeLensOptions{ResolveProvider: false},
				DefinitionProvider:         true,
			},
		}, nil
	case "initialized":
		registerRunnerCapabilities(conn, ctx)
		return nil, nil
	case "shutdown":
		return nil, nil
	case "exit":
		if c, ok := conn.(*jsonrpc2.Conn); ok {
			c.Close()
		}
		return nil, nil
	case "$/cancelRequest":
		return nil, nil
	case "textDocument/didOpen":
		var params lsp.DidOpenTextDocumentParams
		if err := json.Unmarshal(*req.Params, &params); err != nil {
			logger.APILog.Debugf("failed to parse request %s", err.Error())
			return nil, err
		}
		if isGaugeFile(params.TextDocument.URI) {
			openFile(params)
			publishDiagnostics(ctx, conn, params.TextDocument.URI)
		}
		return nil, nil
	case "textDocument/didClose":
		var params lsp.DidCloseTextDocumentParams
		if err := json.Unmarshal(*req.Params, &params); err != nil {
			logger.APILog.Debugf("failed to parse request %s", err.Error())
			return nil, err
		}
		if isGaugeFile(params.TextDocument.URI) {
			closeFile(params)
		}
		return nil, nil
	case "textDocument/didSave":
		return nil, errors.New("Unknown request")
	case "textDocument/didChange":
		var params lsp.DidChangeTextDocumentParams
		if err := json.Unmarshal(*req.Params, &params); err != nil {
			logger.APILog.Debugf("failed to parse request %s", err.Error())
			return nil, err
		}
		if isGaugeFile(params.TextDocument.URI) {
			changeFile(params)
			publishDiagnostics(ctx, conn, params.TextDocument.URI)
		}
		return nil, nil
	case "textDocument/completion":
		return completion(req)
	case "completionItem/resolve":
		return resolveCompletion(req)
	case "textDocument/definition":
		return definition(req)
	case "textDocument/formatting":
		data, err := format(req)
		if err != nil {
			conn.Notify(ctx, "window/showMessage", lsp.ShowMessageParams{Type: 1, Message: err.Error()})
		}
		return data, err
	case "textDocument/codeLens":
		var params lsp.CodeLensParams
		if err := json.Unmarshal(*req.Params, &params); err != nil {
			logger.APILog.Debugf("failed to parse request %s", err.Error())
			return nil, err
		}
		if isGaugeFile(params.TextDocument.URI) {
			return getCodeLenses(params)
		}
		return nil, nil
	case "codeLens/resolve":
		return nil, errors.New("Unknown request")
	case "workspace/symbol":
		return nil, errors.New("Unknown request")
	case "workspace/xreferences":
		return nil, errors.New("Unknown request")
	default:
		return nil, errors.New("Unknown request")
	}
}
func isGaugeFile(uri string) bool {
	return util.IsConcept(uri) || util.IsSpec(uri)
}
func registerRunnerCapabilities(conn jsonrpc2.JSONRPC2, ctx context.Context) {
	var result string
	conn.Call(ctx, "client/registerCapability", registrationParams{[]registration{
		{Id: "js-didOpen", Method: "textDocument/didOpen", RegisterOptions: registerOptions{DocumentSelector: documentSelector{Language: "javascript"}}},
		{Id: "js-didClose", Method: "textDocument/didClose", RegisterOptions: registerOptions{DocumentSelector: documentSelector{Language: "javascript"}}},
		{Id: "js-didChange", Method: "textDocument/didChange", RegisterOptions: registerOptions{DocumentSelector: documentSelector{Language: "javascript"}, SyncKind: lsp.TDSKFull}},
		{Id: "js-codelens", Method: "textDocument/codeLens", RegisterOptions: registerOptions{DocumentSelector: documentSelector{Language: "javascript"}}},
	}}, result)
}

func publishDiagnostics(ctx context.Context, conn jsonrpc2.JSONRPC2, textDocumentUri string) {
	diagnostics := createDiagnostics(textDocumentUri)
	conn.Notify(ctx, "textDocument/publishDiagnostics", lsp.PublishDiagnosticsParams{URI: textDocumentUri, Diagnostics: diagnostics})
}

func (s *server) Start() {
	logger.APILog.Info("LangServer: reading on stdin, writing on stdout")
	var connOpt []jsonrpc2.ConnOpt
	connOpt = append(connOpt, jsonrpc2.LogMessages(log.New(os.Stderr, "", 0)))
	<-jsonrpc2.NewConn(context.Background(), jsonrpc2.NewBufferedStream(stdRWC{}, jsonrpc2.VSCodeObjectCodec{}), newHandler(), connOpt...).DisconnectNotify()
	lRunner.killChan <- true
	logger.APILog.Info("Connection closed")
}

type stdRWC struct{}

func (stdRWC) Read(p []byte) (int, error) {
	return os.Stdin.Read(p)
}

func (stdRWC) Write(p []byte) (int, error) {
	return os.Stdout.Write(p)
}

func (stdRWC) Close() error {
	if err := os.Stdin.Close(); err != nil {
		return err
	}
	return os.Stdout.Close()
}
