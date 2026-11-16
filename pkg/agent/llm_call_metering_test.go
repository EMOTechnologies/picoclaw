// PicoClaw - Ultra-lightweight personal AI agent

package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// llmCallSites lists every place outside pkg/providers that calls an LLM
// (Chat / ChatStream / ChatStreamEvents), keyed by "<file> <function>".
// Each entry says how that call is metered into per-turn usage (turn_usage),
// or why it is exempt.
//
// TestAllLLMCallsAreMetered fails when a call site is added that is not listed
// here, or when a listed one disappears. If it fails after an upstream sync:
// make the new call record usage (usageFromContext(ctx).add(resp.Usage, model)
// or route it through the agent pipeline), then add it here.
var llmCallSites = map[string]string{
	// Main agent loop. Every successful response is recorded at the single
	// ts.usage.add call in Pipeline.CallLLM.
	"pkg/agent/pipeline_llm.go Pipeline.CallLLM":                         "metered: ts.usage.add in Pipeline.CallLLM",
	"pkg/agent/pipeline_streaming.go Pipeline.tryConfiguredStreamingLLM": "metered: response returned to Pipeline.CallLLM",

	// Context management (may run in the background; tracked via backgroundwork).
	"pkg/agent/context_seahorse.go providerToCompleteFn":            "metered: usageFromContext(ctx).add",
	"pkg/agent/context_legacy.go legacyContextManager.retryLLMCall": "metered: turnUsageFromContext(ctx).add",

	// /btw side question; reply is sent by PublishResponseIfNeeded with turn usage.
	"pkg/agent/turn_coord.go AgentLoop.askSideQuestion": "metered: usageFromContext(ctx).add",

	// Voice transcription with an LLM; usage returned in TranscriptionResponse
	// and recorded in AgentLoop.transcribeAudioInMessage.
	"pkg/audio/asr/audio_model_transcriber.go AudioModelTranscriber.Transcribe": "metered: via TranscriptionResponse.Usage",

	// Legacy subagent loop; only used when no SubTurn spawner is set.
	// agent_init.go always sets one, so subagents run through Pipeline.CallLLM.
	"pkg/tools/toolloop.go RunToolLoop": "exempt: unused in production (spawner always set)",

	// Self-evolution; disabled by default (evolution.enabled=false).
	"pkg/evolution/llm_draft_generator.go LLMDraftGenerator.GenerateDraftWithEvidence": "exempt: evolution disabled by default",
	"pkg/evolution/pattern_clusterer.go LLMPatternClusterer.BuildPatterns":             "exempt: evolution disabled by default",
	"pkg/evolution/pattern_clusterer.go LLMPatternClusterer.BuildPatternsWithEvidence": "exempt: evolution disabled by default",
	"pkg/evolution/success_judge.go LLMTaskSuccessJudge.JudgeTaskRecord":               "exempt: evolution disabled by default",
}

var llmCallMethods = map[string]bool{"Chat": true, "ChatStream": true, "ChatStreamEvents": true}

func TestAllLLMCallsAreMetered(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	found := map[string]bool{}
	for _, dir := range []string{"pkg", "cmd", "web"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") ||
				strings.HasPrefix(rel, "pkg/providers/") {
				return nil
			}
			for _, fn := range llmCallingFuncs(t, path) {
				found[rel+" "+fn] = true
			}
			return nil
		})
		if err != nil && !strings.Contains(err.Error(), "no such file") {
			t.Fatal(err)
		}
	}

	var missing, stale []string
	for site := range found {
		if _, ok := llmCallSites[site]; !ok {
			missing = append(missing, site)
		}
	}
	for site := range llmCallSites {
		if !found[site] {
			stale = append(stale, site)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)

	for _, site := range missing {
		t.Errorf("new LLM call site is not metered/listed: %s\n"+
			"  record its usage (usageFromContext(ctx).add) and add it to llmCallSites", site)
	}
	for _, site := range stale {
		t.Errorf("llmCallSites lists %q but no LLM call was found there; update the list", site)
	}
}

// llmCallingFuncs returns the names ("Recv.Method" or "Func") of top-level
// functions in path that contain a .Chat / .ChatStream / .ChatStreamEvents call.
func llmCallingFuncs(t *testing.T, path string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var names []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		calls := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && llmCallMethods[sel.Sel.Name] {
					calls = true
				}
			}
			return !calls
		})
		if calls {
			names = append(names, funcDeclName(fn))
		}
	}
	return names
}

func funcDeclName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	typ := fn.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if idx, ok := typ.(*ast.IndexExpr); ok {
		typ = idx.X
	}
	if ident, ok := typ.(*ast.Ident); ok {
		return ident.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}
