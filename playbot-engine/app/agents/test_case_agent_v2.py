"""
测试用例生成智能体 - 基于LangGraph实现
独立的、无状态的代理包，不依赖宿主的数据库或核心配置
"""
from typing import Dict, Any, List, Optional, Callable, TypedDict, Annotated
import operator
from datetime import datetime
import json
import asyncio
import os
import re

from langgraph.graph import StateGraph, END
from jinja2 import Environment, FileSystemLoader

from .langfuse_utils import get_langfuse_callback_handler
from .schemas import AgentConfig, TestCaseInput
from app.models.semantic_ir import TestPlanBlueprint, TestPlanCase, SemanticStep

class AgentState(TypedDict, total=False):
    input_data: TestCaseInput
    source_code: str
    intent_plan: Any
    
    page_summary: str
    element_whitelist: Dict[str, Any]
    
    blueprint: TestPlanBlueprint
    
    test_cases: Annotated[List[Dict[str, Any]], operator.add]
    logs: Annotated[List[str], operator.add]
    error: Optional[str]

class TestCaseAgent:
    def __init__(self, config: AgentConfig, log_callback: Optional[Callable[[str, str], None]] = None):
        self.config = config
        self.log_callback = log_callback
        self.graph = self._build_graph()
        
        # Setup Jinja2 Environment
        template_dir = os.path.join(os.path.dirname(__file__), 'templates')
        self.jinja_env = Environment(loader=FileSystemLoader(template_dir))
    
    async def _log(self, level: str, message: str):
        timestamp = datetime.now().strftime("%H:%M:%S")
        log_msg = f"[{timestamp}] {message}"
        if self.log_callback:
            await self.log_callback(level, log_msg)
        return log_msg
    
    def _build_graph(self) -> StateGraph:
        async def analyze_context(state: AgentState) -> dict:
            logs_delta = [await self._log("info", "🔬 [步骤1] 正在提炼页面核心功能摘要...")]
            source_code = state.get("source_code")
            if not source_code:
                file_path = state["input_data"].file_path
                if file_path and os.path.exists(file_path):
                    try:
                        with open(file_path, "r", encoding="utf-8") as f:
                            source_code = f.read()
                    except Exception as e:
                        logs_delta.append(await self._log("error", f"❌ 读取源码文件失败: {e}"))
                else:
                    source_code = ""
            
            intent_plan = state.get("intent_plan")
            if not intent_plan:
                json_path = state["input_data"].intent_json_path
                if json_path and os.path.exists(json_path):
                    try:
                        with open(json_path, "r", encoding="utf-8") as f:
                            intent_plan = json.load(f)
                    except:
                        pass

            system_prompt = "你是一个专业的前端功能分析师。请根据提供的 Vue 源码和真实用户的基准交互流（Intent Plan / ActionTrace），用不超过 50 个字的简练中文，总结这个页面的核心功能和交互主体。"
            import json
            user_prompt = f"【Vue源码】\n```\n{source_code[:5000]}\n```\n\n【动作轨迹概览(带DOM片段)】\n{json.dumps(intent_plan, ensure_ascii=False)[:3000]}"
            
            try:
                response = await self.config.llm_caller([
                    {"role": "system", "content": system_prompt}, 
                    {"role": "user", "content": user_prompt}
                ])
                logs_delta.append(await self._log("success", f"✅ 上下文分析完成: {response.strip()}"))
                return {"page_summary": response.strip(), "source_code": source_code, "intent_plan": intent_plan, "logs": logs_delta}
            except Exception as e:
                logs_delta.append(await self._log("error", f"❌ 上下文分析失败: {e}"))
                return {"error": f"上下文分析失败: {e}", "logs": logs_delta}
                
        async def generate_test_plan(state: AgentState) -> dict:
            logs_delta = [await self._log("info", "🧠 [步骤2] 调度大模型输出测试用例规划大纲...")]
            summary = state.get("page_summary", "")
            source_code = state.get("source_code", "")
            action_trace = state.get("intent_plan", {})
            import json
            
            system_prompt = """你是一个专业的自动化测试架构师。
你的任务是根据给定的页面源码、页面核心功能摘要，以及用户录制的真实交互基准流（IntentPlan / ActionTrace），自由发散并设计出最合适的测试用例大纲。

【最高指令红线：信息防丢】：
1. 你的输出将被送入下游的“运行时执行引擎 (Execution Engine)”。你不能生造任何 CSS Selector，你只需提取特征线索。
2. 你必须仔细观察传入的 ActionTrace 记录，如果轨迹目标元素中带有 `recorded_selector` (或者是被映射过来的原 `path`) 和 `dom_fragment`，你必须**原封不动、一字不落**地将其抄写入返回 JSON 的对应字段中！这是后续引擎能够成功防丢兜底的关键！

【核心产出规范】：
1. 【沙盒范围 target_component】: 必须根据 Vue 源码提取该元素所在组件的文件路径（如 src/views/User.vue）。这极其重要，执行引擎将以此限制 DOM 搜索范围！
2. 【目标特征 target_hint】: 提供明确的元素特征字典，包含 text(可见文本), tag(如button, input), role(如button, textbox), placeholder, recorded_selector(原封不动透传), dom_fragment(原封不动透传)。
3. 【上下文特征 context_hint】: 提供父容器或区块特征字典，如 {"parent": "form", "section": "footer"}，帮助引擎在复杂嵌套中定位。
4. 【操作参数 value】: navigate 动作必须在此填入完整的目标 URL（如 https://example.com/page）。fill / select / expect_text 等动作亦用此字段传递参数。切勿将导航 URL 填入 url 字段——url 字段仅供录制元数据使用！
5. 【无断言不测试】: 在每个测试用例的关键操作之后，必须规划出断言步骤（如 expect_visible 或 expect_text）。
6. 考虑异常边界流：除正向流程外，发散构思空值校验、格式错误拦截等异常测试。"""
            
            user_prompt = f"""【页面核心功能摘要】:
{summary}

【录制的基准交互轨迹 (ActionTrace)】:
{json.dumps(action_trace, indent=2, ensure_ascii=False)[:6000]}

【Vue 源码参考】:
{source_code[:6000]}

请根据以上信息，不限数量地输出最完整、最严谨的 TestPlanBlueprint 测试用例规划！"""
            
            try:
                from app.models.semantic_ir import TestPlanBlueprint
                if self.config.structured_llm_caller:
                    blueprint = await self.config.structured_llm_caller(
                        [{"role": "system", "content": system_prompt}, {"role": "user", "content": user_prompt}], 
                        TestPlanBlueprint
                    )
                else:
                    raise Exception("当前模型接口未绑定 structured_llm_caller!")
                    
                cases_count = len(blueprint.test_cases)
                logs_delta.append(await self._log("success", f"✅ 意图规划设计完成，共构思了 {cases_count} 个用例。"))
                return {"blueprint": blueprint, "test_cases": blueprint.test_cases, "logs": logs_delta}
            except Exception as e:
                import traceback
                error_trace = traceback.format_exc()
                logs_delta.append(await self._log("error", f"❌ 意图大纲规划失败: {e}"))
                return {"error": f"意图大纲规划失败: {e}\n{error_trace}", "logs": logs_delta}

        def route_after_step(state: AgentState) -> str:
            if state.get("error"): return "error_end"
            return "next"

        workflow = StateGraph(AgentState)
        workflow.add_node("analyze_context", analyze_context)
        workflow.add_node("generate_test_plan", generate_test_plan)
        
        workflow.set_entry_point("analyze_context")
        
        workflow.add_conditional_edges("analyze_context", route_after_step, {"error_end": END, "next": "generate_test_plan"})
        workflow.add_conditional_edges("generate_test_plan", route_after_step, {"error_end": END, "next": END})
        
        return workflow.compile()
    
    def _create_initial_state(self, input_data: TestCaseInput) -> AgentState:
        return {
            "input_data": input_data,
            "source_code": input_data.source_code or "",
            "intent_plan": input_data.intent_plan,
            "page_summary": "",
            "element_whitelist": {},
            "test_cases": [],
            "logs": [],
            "error": None
        }

    def _get_invoke_config(self):
        lh = get_langfuse_callback_handler(
            public_key=self.config.langfuse_public_key,
            secret_key=self.config.langfuse_secret_key,
            host=self.config.langfuse_host
        )
        return {"callbacks": [lh]} if lh else {}

    async def generate(self, input_data: TestCaseInput) -> Dict[str, Any]:
        await self._log("info", "🚀 绝地反击：工业级 JSON-DSL 意图执行剥离架构启动！...")
        invoke_config = self._get_invoke_config()
        if invoke_config: await self._log("info", "📊 Langfuse 追踪回调已挂载")
        try:
            final_state = await self.graph.ainvoke(self._create_initial_state(input_data), config=invoke_config)
            
            try:
                import langfuse
                langfuse.flush()
                lh = invoke_config.get("callbacks", [None])[0]
                if lh and hasattr(lh, "flush"): lh.flush()
            except: pass
            
            return {
                "test_cases": final_state.get("test_cases", []),
                "analysis": {
                    "code": final_state.get("page_summary", ""),
                    "dom": "白名单已提取: " + str(len(final_state.get("element_whitelist", {}))) + " 个",
                    "strategy": "已使用 JSON 意图图纸编排"
                },
                "generated_count": len(final_state.get("test_cases", [])),
                "error": final_state.get("error")
            }
        except Exception as e:
            if invoke_config: await self._log("error", f"📊 Langfuse 追踪异常退出: {e}")
            raise
