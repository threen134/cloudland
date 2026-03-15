from pydantic_settings import BaseSettings
from typing import Optional
from pydantic import model_validator

class Settings(BaseSettings):
    """
    系统全局配置类 - 使用 Pydantic BaseSettings 自动从环境变量或 .env 文件加载。
    
    包含数据库连接、JWT 安全设置、SMTP 邮件服务、代理转发参数及默认资源配额等。
    """
    PROJECT_NAME: str = "Cloudland Control Plane Gateway"
    API_V1_STR: str = "/api/v1"
    DEBUG: bool = False
    
    # --- 数据库配置 (Database) ---
    # 支持直接提供 DATABASE_URL 或提供 Postgres 各项参数手动拼接。
    POSTGRES_SERVER: Optional[str] = None
    POSTGRES_USER: Optional[str] = None
    POSTGRES_PASSWORD: Optional[str] = None
    POSTGRES_DB: Optional[str] = None
    DATABASE_URL: Optional[str] = None

    # --- JWT & 安全 (Security) ---
    SECRET_KEY: str                                    # HS256 密钥（仅用于激活 Token）
    ALGORITHM: str = "HS256"                           # 激活 Token 算法
    RSA_PRIVATE_KEY_PATH: str = "keys/private.pem"     # RS256 私钥路径（Access Token 签发）
    RSA_PUBLIC_KEY_PATH: str = "keys/public.pem"       # RS256 公钥路径（Access Token 验签）
    ACTIVATION_TOKEN_EXPIRE_HOURS: int = 24
    ACCESS_TOKEN_EXPIRE_MINUTES: int = 120
    
    # --- 超级管理员 (Root Account) ---
    FIRST_SUPERUSER_EMAIL: str
    FIRST_SUPERUSER_USERNAME: str
    FIRST_SUPERUSER_PASSWORD: str
    BACKEND_TOKEN_EXPIRE_SECONDS: int = 7200 # 默认后端 Token 有效期
    
    # --- 高级代理与后端参数 (Proxy & Backend) ---
    DEFAULT_ADMIN_USERNAME: str = "admin"
    BACKEND_API_SUFFIX: str = "/api/v1"
    PROXY_TIMEOUT_SECONDS: float = 30.0
    BACKEND_REQUEST_TIMEOUT: float = 10.0
    TOKEN_REFRESH_BUFFER_MINUTES: int = 5
    AUTO_GENERATED_PW_LENGTH: int = 12

    # --- 通知方式 (Notification) ---
    # "email" = SMTP 邮件, "feishu" = 飞书机器人 Webhook, "both" = 同时发送
    NOTIFICATION_METHOD: str = "email"

    # --- SMTP 邮件服务 (Email) ---
    SMTP_HOST: str = ""
    SMTP_PORT: int = 587
    SMTP_TLS: bool = True
    SMTP_USER: str = ""
    SMTP_PASSWORD: str = ""
    SMTP_FROM: str = ""
    SMTP_FROM_NAME: str = "CloudLand"

    # --- 飞书机器人 (Feishu Bot) ---
    FEISHU_WEBHOOK_URL: str = ""
    FEISHU_SECRET: str = ""

    # --- 应用部署 URL (App URLs) ---
    FRONTEND_URL: str
    LISTEN_ADDR: str = "0.0.0.0"
    LISTEN_PORT: int = 8000
    
    # --- 默认资源配额 (Resource Quotas) ---
    DEFAULT_CPU_CORES: float = 4.0
    DEFAULT_RAM_GB: float = 8.0
    DEFAULT_TRAFFIC_GB: float = 100.0
    DEFAULT_PUBLIC_IPS: int = 2
    DEFAULT_DISK_GB: float = 50.0

    @model_validator(mode='after')
    def check_db_config(self):
        """验证数据库配置是否完整"""
        if not self.DATABASE_URL:
            if not all([self.POSTGRES_SERVER, self.POSTGRES_USER, self.POSTGRES_PASSWORD, self.POSTGRES_DB]):
                raise ValueError("Either DATABASE_URL or all POSTGRES_* variables must be provided")
        return self

    @property
    def async_database_url(self) -> str:
        if self.DATABASE_URL:
            return self.DATABASE_URL
        # We know these exist because of the validator above
        return f"postgresql+asyncpg://{self.POSTGRES_USER}:{self.POSTGRES_PASSWORD}@{self.POSTGRES_SERVER}/{self.POSTGRES_DB}"

    class Config:
        case_sensitive = True
        env_file = ".env"

settings = Settings()
