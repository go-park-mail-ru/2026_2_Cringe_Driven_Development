"""Configure a private Jupyter Server with agent-managed restarts."""

# Файл запекается в образ; конфигурация с хоста не монтируется.
c = get_config()  # noqa: F821
c.ServerApp.ip = "0.0.0.0"
c.ServerApp.port = 8888
c.ServerApp.port_retries = 0
c.ServerApp.open_browser = False
c.ServerApp.root_dir = "/workspace"
c.ServerApp.terminals_enabled = False
c.ServerApp.jpserver_extensions = {}
c.ServerApp.allow_remote_access = True
# Лимиты на output и его вычитку держит агент, Jupyter не должен молча терять сообщения.
c.ZMQChannelsWebsocketConnection.iopub_data_rate_limit = 0
c.ZMQChannelsWebsocketConnection.iopub_msg_rate_limit = 0
# Оставляем наблюдатель смерти, но никогда не перезапускаем ядро пользователя автоматически.
# Полное отключение autorestart отключило бы и уведомления о смерти ядра.
c.KernelManager.autorestart = True
c.KernelRestarter.restart_limit = 0
c.KernelRestarter.time_to_dead = 1.0
