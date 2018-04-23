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
	"fmt"
	"reflect"
	"sync"

	"github.com/getgauge/common"
	"github.com/getgauge/gauge/gauge"
	gm "github.com/getgauge/gauge/gauge_messages"
	"github.com/getgauge/gauge/parser"
	"github.com/getgauge/gauge/util"
	"github.com/getgauge/gauge/validation"
	"github.com/sourcegraph/go-langserver/pkg/lsp"
	"github.com/sourcegraph/jsonrpc2"
)

// Diagnostics lock ensures only one goroutine publishes diagnostics at a time.
var diagnosticsLock sync.Mutex

// isInQueue ensures that only one other goroutine waits for the diagnostic lock.
// Since diagnostics are published for all files, multiple threads need not wait to publish diagnostics.
var isInQueue = false

type diagnosticsMap struct {
	diagnostics map[lsp.DocumentURI][]lsp.Diagnostic
}

func (dm diagnosticsMap) add(uri lsp.DocumentURI, d lsp.Diagnostic) {
	if !dm.contains(uri, d) {
		dm.diagnostics[uri] = append(dm.diagnostics[uri], d)
	}
}

func (dm diagnosticsMap) contains(uri lsp.DocumentURI, d lsp.Diagnostic) bool {
	for _, diagnostic := range dm.diagnostics[uri] {
		if reflect.DeepEqual(diagnostic, d) {
			return true
		}
	}
	return false
}

func (dm diagnosticsMap) hasURI(uri lsp.DocumentURI) bool {
	if _, ok := dm.diagnostics[uri]; !ok {
		return false
	}
	return false
}

func publishDiagnostics(ctx context.Context, conn jsonrpc2.JSONRPC2) {
	defer recoverPanic(nil)
	if !isInQueue {
		isInQueue = true

		diagnosticsLock.Lock()
		defer diagnosticsLock.Unlock()

		isInQueue = false

		diagnosticsMap, err := getDiagnostics()
		if err != nil {
			logError(nil, "Unable to publish diagnostics, error : %s", err.Error())
			return
		}
		for uri, diagnostics := range diagnosticsMap {
			publishDiagnostic(uri, diagnostics, conn, ctx)
		}
	}
}

func publishDiagnostic(uri lsp.DocumentURI, diagnostics []lsp.Diagnostic, conn jsonrpc2.JSONRPC2, ctx context.Context) {
	params := lsp.PublishDiagnosticsParams{URI: uri, Diagnostics: diagnostics}
	conn.Notify(ctx, "textDocument/publishDiagnostics", params)
}

func getDiagnostics() (map[lsp.DocumentURI][]lsp.Diagnostic, error) {
	diagnostics := diagnosticsMap{diagnostics: make(map[lsp.DocumentURI][]lsp.Diagnostic, 0)}
	conceptDictionary, err := validateConcepts(diagnostics)
	if err != nil {
		return nil, err
	}
	if err = validateSpecs(conceptDictionary, diagnostics); err != nil {
		return nil, err
	}
	return diagnostics.diagnostics, nil
}

func createValidationDiagnostics(errors []validation.StepValidationError, diagnostics diagnosticsMap) {
	for _, err := range errors {
		uri := util.ConvertPathToURI(err.FileName())
		d := createDiagnostic(uri, err.Message(), err.Step().LineNo-1, 1)
		if err.ErrorType() == gm.StepValidateResponse_STEP_IMPLEMENTATION_NOT_FOUND {
			d.Code = err.Suggestion()
		}
		diagnostics.add(uri, d)
	}
	return
}

func validateSpec(spec *gauge.Specification, conceptDictionary *gauge.ConceptDictionary) (vErrors []validation.StepValidationError) {
	if lRunner.runner == nil {
		return
	}
	v := validation.NewSpecValidator(spec, lRunner.runner, conceptDictionary, []error{}, map[string]error{})
	for _, e := range v.Validate() {
		vErrors = append(vErrors, e.(validation.StepValidationError))
	}
	return
}

func validateSpecs(conceptDictionary *gauge.ConceptDictionary, diagnostics diagnosticsMap) error {
	specFiles := util.GetSpecFiles(util.GetSpecDirs())
	for _, specFile := range specFiles {
		uri := util.ConvertPathToURI(specFile)
		if !diagnostics.hasURI(uri) {
			diagnostics.diagnostics[uri] = make([]lsp.Diagnostic, 0)
		}
		content, err := getContentFromFileOrDisk(specFile)
		if err != nil {
			return fmt.Errorf("Unable to read file %s", err)
		}
		spec, res, err := new(parser.SpecParser).Parse(content, conceptDictionary, specFile)
		if err != nil {
			return err
		}
		createDiagnostics(res, diagnostics)
		if res.Ok {
			createValidationDiagnostics(validateSpec(spec, conceptDictionary), diagnostics)
		}
	}
	return nil
}

func validateConcepts(diagnostics diagnosticsMap) (*gauge.ConceptDictionary, error) {
	conceptFiles := util.GetConceptFiles()
	conceptDictionary := gauge.NewConceptDictionary()
	for _, conceptFile := range conceptFiles {
		uri := util.ConvertPathToURI(conceptFile)
		if !diagnostics.hasURI(uri) {
			diagnostics.diagnostics[uri] = make([]lsp.Diagnostic, 0)
		}
		content, err := getContentFromFileOrDisk(conceptFile)
		if err != nil {
			return nil, fmt.Errorf("Unable to read file %s", err)
		}
		cpts, pRes := new(parser.ConceptParser).Parse(content, conceptFile)
		pErrs, err := parser.AddConcept(cpts, conceptFile, conceptDictionary)
		if err != nil {
			return nil, err
		}
		pRes.ParseErrors = append(pRes.ParseErrors, pErrs...)
		createDiagnostics(pRes, diagnostics)
	}
	createDiagnostics(parser.ValidateConcepts(conceptDictionary), diagnostics)
	return conceptDictionary, nil
}

func createDiagnostics(res *parser.ParseResult, diagnostics diagnosticsMap) {
	for _, err := range res.ParseErrors {
		uri := util.ConvertPathToURI(err.FileName)
		diagnostics.add(uri, createDiagnostic(uri, err.Message, err.LineNo-1, 1))
	}
	for _, warning := range res.Warnings {
		uri := util.ConvertPathToURI(warning.FileName)
		diagnostics.add(uri, createDiagnostic(uri, warning.Message, warning.LineNo-1, 2))
	}
}

func createDiagnostic(uri lsp.DocumentURI, message string, line int, severity lsp.DiagnosticSeverity) lsp.Diagnostic {
	endChar := 10000
	if isOpen(uri) {
		endChar = len(getLine(uri, line))
	}
	return lsp.Diagnostic{
		Range: lsp.Range{
			Start: lsp.Position{Line: line, Character: 0},
			End:   lsp.Position{Line: line, Character: endChar},
		},
		Message:  message,
		Severity: severity,
	}
}

func getContentFromFileOrDisk(fileName string) (string, error) {
	uri := util.ConvertPathToURI(fileName)
	if isOpen(uri) {
		return getContent(uri), nil
	} else {
		return common.ReadFileContents(fileName)
	}
}
