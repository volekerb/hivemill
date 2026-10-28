"""Run the visible and hidden unit tests in the current directory."""
import subprocess
import sys

try:
    proc = subprocess.run(
        [sys.executable, "-m", "unittest", "discover", "-s", ".", "-p", "test*.py"],
        capture_output=True, text=True, timeout=50)
except subprocess.TimeoutExpired:
    print("FAIL: tests timed out")
    sys.exit(1)
if proc.returncode == 0:
    print("PASS: all tests pass")
    sys.exit(0)
tail = (proc.stderr or proc.stdout).strip().splitlines()[-3:]
print("FAIL: tests failed: " + " | ".join(tail))
sys.exit(1)
