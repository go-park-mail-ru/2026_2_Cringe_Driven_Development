"""Run inside a fresh kernel container; emit sampled tmpfs and cgroup peaks as JSON."""

import json
import os
import platform
import site
import threading
from importlib.metadata import version
from pathlib import Path

from IPython.core.interactiveshell import InteractiveShell

stop = threading.Event()
peaks = dict.fromkeys(("/tmp", "/packages", "/workspace"), 0)
errors = []


def sample():
    while True:
        for path in peaks:
            stat = os.statvfs(path)
            peaks[path] = max(peaks[path], (stat.f_blocks - stat.f_bfree) * stat.f_frsize)
        if stop.wait(0.1):
            return


monitor = threading.Thread(target=sample)
monitor.start()
try:
    # The real kernel launcher includes the initially empty user site as well.
    site.addsitedir(site.getusersitepackages())
    shell = InteractiveShell.instance()
    result = shell.run_cell("%pip install -q numpy matplotlib pandas scikit-learn")
    if shell.user_ns.get("_exit_code", 0):
        errors.append(f"pip exit code: {shell.user_ns['_exit_code']}")
    if result.error_in_exec or result.error_before_exec:
        errors.append(str(result.error_in_exec or result.error_before_exec))
    result = shell.run_cell("import numpy; import matplotlib.pyplot; import pandas; import sklearn")
    if result.error_in_exec or result.error_before_exec:
        errors.append(str(result.error_in_exec or result.error_before_exec))
finally:
    stop.set()
    monitor.join()
    cgroup = Path("/sys/fs/cgroup")
    print(
        json.dumps(
            {
                "python": platform.python_version(),
                "versions": {
                    name: version(name)
                    for name in ("numpy", "matplotlib", "pandas", "scikit-learn", "scipy")
                    if __import__("importlib.util", fromlist=["find_spec"]).find_spec(
                        {"scikit-learn": "sklearn"}.get(name, name)
                    )
                },
                "sample_interval_seconds": 0.1,
                "sampled_peak_bytes": peaks,
                "memory_peak_bytes": (cgroup / "memory.peak").read_text().strip(),
                "memory_events": (cgroup / "memory.events").read_text(),
                "errors": errors,
            },
            indent=2,
        )
    )
if errors:
    raise SystemExit(1)
