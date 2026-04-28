import logging
import asyncio
import os

from app.core.config import settings

# 根据配置动态引入 OpenAI 客户端以支持自动追踪
if settings.langfuse_enabled and settings.langfuse_public_key and settings.langfuse_secret_key:
    os.environ["LANGFUSE_PUBLIC_KEY"] = settings.langfuse_public_key
    os.environ["LANGFUSE_SECRET_KEY"] = settings.langfuse_secret_key
    if settings.langfuse_host:
        os.environ["LANGFUSE_HOST"] = settings.langfuse_host
        
    from langfuse.openai import AsyncOpenAI
else:
    from openai import AsyncOpenAI

logger = logging.getLogger(__name__)

# LLM调用超时时间（秒）- 生成用例可能需要更长时间
LLM_TIMEOUT = 120  # 从30秒增加到120秒（2分钟）


async def _get_llm_config() -> dict:
    """Load LLM config from settings."""
    return {
        "endpoint": settings.llm_endpoint,
        "api_key": settings.llm_api_key,
        "model": settings.llm_model,
    }


async def get_llm_client() -> tuple[AsyncOpenAI, str]:
    """Return an OpenAI-compatible async client and the model name."""
    cfg = await _get_llm_config()
    import httpx
    client = AsyncOpenAI(
        base_url=cfg["endpoint"], 
        api_key=cfg["api_key"],
        timeout=httpx.Timeout(LLM_TIMEOUT, connect=10.0)
    )
    return client, cfg["model"]


async def get_langchain_chat_model(temperature: float = 0.2, max_tokens: int = 8192):
    """Return a LangChain ChatOpenAI compatible model."""
    from langchain_openai import ChatOpenAI
    cfg = await _get_llm_config()
    
    # LangChain ChatOpenAI model
    model = ChatOpenAI(
        model=cfg["model"],
        api_key=cfg["api_key"],
        base_url=cfg["endpoint"],
        temperature=temperature,
        max_tokens=max_tokens,
        timeout=LLM_TIMEOUT,
    )
    return model


async def llm_chat(
    messages: list[dict],
    temperature: float = 0.3,
    max_tokens: int = 8192,
) -> str:
    """Send a chat completion request and return the assistant message content."""
    client, model = await get_llm_client()
    response = await client.chat.completions.create(
        model=model,
        messages=messages,
        temperature=temperature,
        max_tokens=max_tokens,
    )
    return response.choices[0].message.content or ""


async def llm_chat_stream(
    messages: list[dict],
    temperature: float = 0.3,
    max_tokens: int = 8192,
    on_token: callable = None,
) -> str:
    """
    Send a streaming chat completion request.
    on_token callback receives each token as it arrives.
    """
    client, model = await get_llm_client()
    stream = await client.chat.completions.create(
        model=model,
        messages=messages,
        temperature=temperature,
        max_tokens=max_tokens,
        stream=True,  # 启用流式输出
    )
    
    full_content = []
    chunk_count = 0
    async for chunk in stream:
        chunk_count += 1
        
        # 尝试多种方式获取内容
        if chunk.choices:
            choice = chunk.choices[0]
            if hasattr(choice, 'delta'):
                delta = choice.delta
                # 方式1: delta.content (标准流式)
                token = None
                if hasattr(delta, 'content') and delta.content:
                    token = delta.content
                # 方式2: delta.reasoning_content (智谱GLM等模型)
                elif hasattr(delta, 'reasoning_content') and delta.reasoning_content:
                    token = delta.reasoning_content
                
                if token:
                    full_content.append(token)
                    if on_token:
                        await on_token(token)
    
    print(f"[LLM-Stream] Total chunks: {chunk_count}, Total content length: {len(''.join(full_content))}", flush=True)
    return "".join(full_content)


async def llm_chat_json(
    messages: list[dict],
    temperature: float = 0.2,
    max_tokens: int = 4096,
) -> str:
    """Send a chat request with JSON response format."""
    client, model = await get_llm_client()
    response = await client.chat.completions.create(
        model=model,
        messages=messages,
        temperature=temperature,
        max_tokens=max_tokens,
        response_format={"type": "json_object"},
    )
    return response.choices[0].message.content or "{}"


async def verify_llm_connection(
    endpoint: str,
    api_key: str,
    model: str,
) -> tuple[bool, str, str, list[dict]]:
    """
    验证 LLM 连接是否正常
    返回: (是否成功, 消息, 模型名称, 交互记录)
    """
    interaction_log = []
    
    try:
        import httpx
        client = AsyncOpenAI(
            base_url=endpoint, 
            api_key=api_key,
            timeout=httpx.Timeout(10.0, connect=5.0)
        )
        
        # 记录请求信息
        request_info = {
            "type": "request",
            "endpoint": endpoint,
            "model": model,
            "messages": [{"role": "user", "content": "Hi"}],
            "max_tokens": 10,
            "temperature": 0.1,
        }
        interaction_log.append(request_info)
        
        # 发送一个简单的测试请求
        response = await client.chat.completions.create(
            model=model,
            messages=[
                {"role": "user", "content": "Hi"}
            ],
            max_tokens=10,
            temperature=0.1,
        )
        
        # 记录响应信息
        response_info = {
            "type": "response",
            "status": "success",
            "model": response.model if hasattr(response, 'model') else model,
            "usage": {
                "prompt_tokens": response.usage.prompt_tokens if response.usage else 0,
                "completion_tokens": response.usage.completion_tokens if response.usage else 0,
                "total_tokens": response.usage.total_tokens if response.usage else 0,
            } if response.usage else {},
            "content": response.choices[0].message.content if response.choices else "",
        }
        interaction_log.append(response_info)
        
        # 检查是否有响应
        if response.choices and len(response.choices) > 0:
            logger.info(f"LLM 连接验证成功: {endpoint}, 模型: {model}")
            return True, f"连接成功！模型 {model} 响应正常", model, interaction_log
        else:
            response_info["status"] = "error"
            response_info["error"] = "模型未返回有效响应"
            return False, "连接失败：模型未返回有效响应", model, interaction_log
            
    except Exception as e:
        error_msg = str(e)
        logger.error(f"LLM 连接验证失败: {error_msg}")
        
        # 记录错误信息
        error_info = {
            "type": "error",
            "error": error_msg,
        }
        interaction_log.append(error_info)
        
        # 提供更友好的错误信息
        if "401" in error_msg or "Unauthorized" in error_msg or "invalid_api_key" in error_msg.lower():
            return False, "API Key 无效，请检查后重试", model, interaction_log
        elif "404" in error_msg or "model_not_found" in error_msg.lower():
            return False, f"模型 {model} 不存在，请检查模型名称", model, interaction_log
        elif "403" in error_msg or "Forbidden" in error_msg:
            return False, "API Key 权限不足，请检查账户状态", model, interaction_log
        elif "Connection" in error_msg or "connection" in error_msg.lower():
            return False, "无法连接到服务端，请检查网络或端点地址", model, interaction_log
        else:
            return False, f"连接失败: {error_msg}", model, interaction_log
