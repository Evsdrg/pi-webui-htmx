package runtime

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// toolsExtHarness 用 node 加载内嵌的工具集扩展，喂给它一个假的 pi 对象与一组事件，
// 把扩展改写后的结果以 JSON 打印出来。缺 node 时跳过（与 sqlite3 用例同一惯例）。
const toolsExtHarness = `
import { pathToFileURL } from "node:url";
const mod = await import(pathToFileURL(process.argv[2]).href);
const handlers = {};
const pi = {
  on(name, fn) { (handlers[name] ??= []).push(fn); return () => {}; },
  getAllTools() { return [{ name: "bash" }, { name: "read" }]; },
  setActiveTools() {},
};
mod.default(pi);
let threw = null;
const fire = async (name, event) => {
  for (const fn of handlers[name] ?? []) {
    try { await fn(event, {}); } catch (error) { threw = String(error); }
  }
  return event;
};
const call = (toolName, input, extra = {}) => ({ type: "tool_call", toolCallId: "c1", toolName, input, ...extra });
const out = {};
out.bashMissing = (await fire("tool_call", call("bash", { command: "sleep 1" }))).input;
out.bashNull = (await fire("tool_call", call("bash", { command: "sleep 1", timeout: null }))).input;
out.bashExplicit = (await fire("tool_call", call("bash", { command: "make", timeout: 900 }))).input;
out.powershell = (await fire("tool_call", call("powershell", { command: "Get-Date" }))).input;
out.nested = (await fire("tool_call", call("bash", { command: "ls" }, { parentToolCallId: "p1" }))).input;
out.read = (await fire("tool_call", call("read", { path: "a.txt" }))).input;
await fire("tool_call", null);
await fire("tool_call", { type: "tool_call", toolName: "bash" });
await fire("tool_call", call("bash", "not-an-object"));
const options = { selectedTools: ["bash", "read"], toolGuidelines: { bash: ["existing rule"] } };
const startEvent = { type: "before_agent_start", systemPrompt: "", systemPromptOptions: options };
await fire("before_agent_start", startEvent);
await fire("before_agent_start", startEvent);
out.guidelines = options.toolGuidelines;
const bare = { selectedTools: ["read"] };
await fire("before_agent_start", { type: "before_agent_start", systemPrompt: "", systemPromptOptions: bare });
out.bareGuidelines = bare.toolGuidelines ?? null;
await fire("before_agent_start", { type: "before_agent_start", systemPrompt: "" });
out.threw = threw;
console.log(JSON.stringify(out));
`

type toolsExtResult struct {
	BashMissing    map[string]any      `json:"bashMissing"`
	BashNull       map[string]any      `json:"bashNull"`
	BashExplicit   map[string]any      `json:"bashExplicit"`
	Powershell     map[string]any      `json:"powershell"`
	Nested         map[string]any      `json:"nested"`
	Read           map[string]any      `json:"read"`
	Guidelines     map[string][]string `json:"guidelines"`
	BareGuidelines map[string][]string `json:"bareGuidelines"`
	Threw          *string             `json:"threw"`
}

func runToolsExtHarness(t *testing.T) toolsExtResult {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("缺少 node，跳过工具集扩展行为测试")
	}
	dir := t.TempDir()
	extPath, err := InstallToolsExt(dir)
	if err != nil {
		t.Fatalf("落盘扩展失败: %v", err)
	}
	harness := filepath.Join(dir, "harness.mjs")
	if err := os.WriteFile(harness, []byte(toolsExtHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := exec.Command(node, harness, extPath).CombinedOutput()
	if err != nil {
		t.Fatalf("harness 失败: %v\n%s", err, raw)
	}
	var result toolsExtResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("harness 输出不是 JSON: %v\n%s", err, raw)
	}
	return result
}

// Test工具集扩展给命令补默认超时 钉住「模型没传 timeout 的 shell 命令一律 300 秒兜底」：
// Pi 的 bash/powershell 只在模型显式传 timeout 时才计时，否则一条挂起的命令会让会话永久卡住。
func Test工具集扩展给命令补默认超时(t *testing.T) {
	r := runToolsExtHarness(t)
	if r.Threw != nil {
		t.Fatalf("扩展处理器不应抛异常（tool_call 处理器抛错会让 Pi 拦下工具调用）: %s", *r.Threw)
	}
	for name, input := range map[string]map[string]any{"bash": r.BashMissing, "timeout 为 null": r.BashNull, "powershell": r.Powershell, "嵌套调用": r.Nested} {
		if input["timeout"] != float64(300) {
			t.Fatalf("%s 缺省 timeout 应补成 300，得到 %v", name, input)
		}
	}
	if r.BashExplicit["timeout"] != float64(900) {
		t.Fatalf("模型显式给的 timeout 不应被改写: %v", r.BashExplicit)
	}
	if _, ok := r.Read["timeout"]; ok {
		t.Fatalf("非 shell 工具不应被加 timeout: %v", r.Read)
	}
}

// Test工具集扩展提醒长任务指定超时 钉住提示词准则：只挂在 shell 工具下、保留 Pi 原有准则、
// 多轮注入不重复，且明确告诉模型默认值与「长任务须自己指定」。
func Test工具集扩展提醒长任务指定超时(t *testing.T) {
	r := runToolsExtHarness(t)
	bash := r.Guidelines["bash"]
	if len(bash) != 2 || bash[0] != "existing rule" {
		t.Fatalf("bash 准则应是原有一条 + 新增一条（两次注入不重复），得到 %q", bash)
	}
	if !strings.Contains(bash[1], "300") || !strings.Contains(bash[1], "timeout") {
		t.Fatalf("新增准则应说明 300 秒默认值并要求长任务传 timeout: %q", bash[1])
	}
	if got := r.Guidelines["powershell"]; len(got) != 1 || got[0] != bash[1] {
		t.Fatalf("powershell 应挂同一句准则（Pi 按文本去重，只渲染一次）: %q", got)
	}
	if r.BareGuidelines == nil || len(r.BareGuidelines["bash"]) != 1 {
		t.Fatalf("没有原有准则时也应建出 bash 准则: %v", r.BareGuidelines)
	}
}
