import argparse
import asyncio
import json
import os
import sys

# 将当前目录加入 PYTHONPATH，以便导入 app.xxx
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from app.core.config import settings
from app.agents import TestCaseAgent, AgentConfig, TestCaseInput
from app.services.llm_service import llm_chat_stream, get_langchain_chat_model

async def streaming_llm_caller(messages):
    buffer = []
    async def on_token_callback(token_text: str):
        buffer.append(token_text)
        # 不输出到标准输出，避免污染最终的 JSON 结果
        # 如果需要进度，可以打印到 stderr
        print(token_text, end="", file=sys.stderr, flush=True)
    return await llm_chat_stream(messages, on_token=on_token_callback)

async def structured_wrapper(messages, target_schema):
    llm = await get_langchain_chat_model()
    structured_llm = llm.with_structured_output(target_schema)
    print("\n[CLI] Planning via schema...", file=sys.stderr, flush=True)
    try:
        return await structured_llm.ainvoke(messages)
    except Exception as e:
        print(f"\n[CLI] Structured output failed, falling back: {e}", file=sys.stderr, flush=True)
        # Fallback raw parsing
        import json_repair
        schema_json = json.dumps(target_schema.model_json_schema(), ensure_ascii=False)
        fallback_msgs = list(messages)
        fallback_txt = f"\n\n【系统修正惩罚】：请直接输出原生合法的 JSON 字符串，遵守下方 Schema 限定:\n{schema_json}"
        if fallback_msgs and fallback_msgs[-1]["role"] == "user":
            fallback_msgs[-1] = {"role": "user", "content": str(fallback_msgs[-1]["content"]) + fallback_txt}
        else:
            fallback_msgs.append({"role": "user", "content": fallback_txt})
        
        raw_content = await streaming_llm_caller(fallback_msgs)
        try:
            repaired_json_str = json_repair.repair_json(raw_content)
            parsed_obj = json.loads(repaired_json_str)
            if isinstance(parsed_obj, list) and len(parsed_obj) > 0:
                parsed_obj = parsed_obj[0]
            return target_schema.model_validate(parsed_obj)
        except Exception as e2:
            raise Exception(f"Failed to parse repaired json: {e2}")

async def log_callback(level: str, message: str):
    print(f"[{level.upper()}] {message}", file=sys.stderr, flush=True)

async def main():
    parser = argparse.ArgumentParser(description="Playbot Engine CLI")
    parser.add_argument("--input", required=True, help="Path to input JSON file containing intent_plan and snapshot")
    parser.add_argument("--llm-endpoint", default="https://api.deepseek.com/v1", help="LLM API Endpoint")
    parser.add_argument("--llm-api-key", required=True, help="LLM API Key")
    parser.add_argument("--llm-model", default="deepseek-coder", help="LLM Model name")
    
    args = parser.parse_args()

    # 动态覆盖配置
    settings.llm_endpoint = args.llm_endpoint
    settings.llm_api_key = args.llm_api_key
    settings.llm_model = args.llm_model

    with open(args.input, "r", encoding="utf-8") as f:
        job_data = json.load(f)

    # Browserwing 将页面快照放在 "snapshot"，轨迹放在 "intent_plan"
    page_url = job_data.get("page_url", "http://localhost")
    intent_plan = job_data.get("intent_plan", {})
    snapshot = job_data.get("snapshot", {})

    # 我们将 snapshot 序列化为 source_code，完美替换原本的 Vue 源码
    source_code = json.dumps(snapshot, ensure_ascii=False, indent=2)

    config = AgentConfig(
        llm_caller=streaming_llm_caller,
        structured_llm_caller=structured_wrapper
    )

    agent = TestCaseAgent(config=config, log_callback=log_callback)
    
    input_data = TestCaseInput(
        page_url=page_url,
        source_code=source_code,
        intent_plan=intent_plan
    )

    result = await agent.generate(input_data)
    
    # 转换为 dict 以便序列化
    if "test_cases" in result:
        # result["test_cases"] 是 TestPlanCase 的 Pydantic 列表
        result["test_cases"] = [tc.model_dump() for tc in result["test_cases"]]

    # 最终结果只能打印到 stdout
    print(json.dumps(result, ensure_ascii=False, indent=2))

if __name__ == "__main__":
    asyncio.run(main())
