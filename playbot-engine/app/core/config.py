import os
from pathlib import Path
from pydantic_settings import BaseSettings


class Settings(BaseSettings):
    # Application
    app_name: str = "Playwright Test Platform"
    debug: bool = True

    # Paths
    base_dir: Path = Path(__file__).resolve().parent.parent.parent
    workspace_dir: Path = base_dir.parent / "workspace"
    repos_dir: Path = workspace_dir / "repos"
    tests_dir: Path = workspace_dir / "tests"

    # Database
    database_url: str = f"sqlite+aiosqlite:///{base_dir.parent / 'workspace' / 'data.db'}"

    # LLM (OpenAI-compatible API)
    llm_endpoint: str = "https://api.openai.com/v1"
    llm_api_key: str = ""
    llm_model: str = "gpt-4o"

    # Server
    host: str = "0.0.0.0"
    port: int = 8004

    # CORS
    cors_origins: list[str] = ["http://localhost:5174", "http://localhost:3000"]

    # Langfuse Configuration (Open Source LLM Observability)
    langfuse_enabled: bool = False
    langfuse_public_key: str = ""
    langfuse_secret_key: str = ""
    langfuse_host: str = "http://localhost:3000"  # Langfuse本地部署地址

    model_config = {"env_prefix": "PTP_", "env_file": ".env"}


settings = Settings()

# Ensure workspace directories exist
settings.repos_dir.mkdir(parents=True, exist_ok=True)
settings.tests_dir.mkdir(parents=True, exist_ok=True)
