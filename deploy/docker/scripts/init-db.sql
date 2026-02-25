-- CloudLand Docker PostgreSQL 初始化脚本
-- 此脚本在 postgres 容器首次启动时自动执行
-- (通过 docker-entrypoint-initdb.d 机制)

-- 数据库已通过环境变量 POSTGRES_DB 创建，此处做额外初始化（如需要）
-- 若需要创建额外用户或授权，可在此添加

-- 确保扩展可用
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
