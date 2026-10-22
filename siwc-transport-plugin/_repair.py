import io
import re

p = "internal/pluginrpc/server.go"
t = open(p, encoding="ascii").read()
t = re.sub(r"// InitHostServices\S*package pluginrpc",
           "// InitHostServices dials the host broker.\npackage pluginrpc", t, count=1)
io.open(p, "w", encoding="utf-8", newline="\n").write(t)
print("fixed, has package line:", "package pluginrpc" in t.split("\n")[3] or "package pluginrpc" in t.split("\n")[2])
