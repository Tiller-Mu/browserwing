"""
Langfuse 工具函数 - 用于追踪和分析智能体执行
开源LLM可观测性平台: https://langfuse.com
"""
import os
from typing import Optional

def get_langfuse_callback_handler(public_key: Optional[str] = None, secret_key: Optional[str] = None, host: Optional[str] = None):
    """获取 Langfuse Callback Handler，用于 LangGraph / LangChain 集成"""
    try:
        pk = public_key or os.environ.get("LANGFUSE_PUBLIC_KEY")
        sk = secret_key or os.environ.get("LANGFUSE_SECRET_KEY")
        h = host or os.environ.get("LANGFUSE_HOST", "https://cloud.langfuse.com")
        
        if not pk or not sk:
            return None
            
        from langfuse.langchain import CallbackHandler
        return CallbackHandler(public_key=pk, secret_key=sk, host=h)
    except Exception as e:
        import logging
        logging.getLogger(__name__).warning(f"Langfuse callback handler failed to initialize: {e}")
        return None
