"""Configure a private Jupyter Server with agent-managed restarts."""

# This file is baked into the image; no host configuration is mounted.
import os
import site

c = get_config()  # noqa: F821
# Python adds the user site to sys.path only if it exists when the kernel starts,
# so %pip install --user packages become importable without a kernel restart.
os.makedirs(site.getusersitepackages(), exist_ok=True)
c.ServerApp.ip = "0.0.0.0"
c.ServerApp.port = 8888
c.ServerApp.port_retries = 0
c.ServerApp.open_browser = False
c.ServerApp.root_dir = "/workspace"
c.ServerApp.terminals_enabled = False
c.ServerApp.jpserver_extensions = {}
c.ServerApp.allow_remote_access = True
# Agent budgets and drains output. Jupyter must not silently discard messages.
c.ZMQChannelsWebsocketConnection.iopub_data_rate_limit = 0
c.ZMQChannelsWebsocketConnection.iopub_msg_rate_limit = 0
# Keep the death watcher, but never automatically restart a user's kernel.
# Disabling autorestart entirely would also disable death notifications.
c.KernelManager.autorestart = True
c.KernelRestarter.restart_limit = 0
c.KernelRestarter.time_to_dead = 1.0
