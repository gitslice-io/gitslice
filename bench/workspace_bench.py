#!/usr/bin/env python3
"""Time to a usable workspace: git clones against gs init and gs init --lazy.

For one repository that exists both on GitHub and as a Gitslice slice, each
scenario is run RUNS times from a cold start (a new directory and, for gs, an
empty client cache) and the median is reported:

  time to tree    the command returns and the files are there to open
  read 20 files   reading 20 files spread evenly over the tree, as an agent
                  that opens a few of them to do a task would (for gs --lazy
                  this fetches them; git and gs init already hold them)
  search cold     ripgrep over the whole tree right after: the worst case for
                  a lazy workspace, which has to fetch every file it has not
                  read yet
  search warm     the same search again

Usage:
  bench/workspace_bench.py --git https://github.com/org/repo --slice account/slice \\
      [--runs 5] [--gs gs] [--pattern TODO] [--json out.json]

Needs git, ripgrep, a gs that is signed in (HOME's config, copied, never
modified), and FUSE for the lazy scenario. Everything it writes goes under a
temporary directory that is removed afterwards. Times are wall clock from this
machine, so the network distance to GitHub and to the Gitslice server is part
of the result; the script prints both round trips.
"""
import argparse
import json
import os
import shutil
import statistics
import subprocess
import sys
import tempfile
import time
import urllib.parse


def run(cmd, cwd=None, env=None, check=True):
    start = time.perf_counter()
    proc = subprocess.run(cmd, cwd=cwd, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    elapsed = time.perf_counter() - start
    if check and proc.returncode != 0:
        sys.exit(f"{' '.join(cmd)} failed ({proc.returncode}):\n{proc.stderr[-2000:]}")
    return elapsed, proc


def rtt(url):
    host = urllib.parse.urlparse(url).netloc
    out = subprocess.run(["curl", "-s", "-o", "/dev/null", "-w", "%{time_connect}", f"https://{host}"], stdout=subprocess.PIPE, text=True)
    return float(out.stdout or 0)


def tree_files(root):
    out = []
    for dirpath, dirnames, filenames in os.walk(root):
        dirnames[:] = sorted(d for d in dirnames if d not in (".git", ".gs"))
        for name in sorted(filenames):
            path = os.path.join(dirpath, name)
            if os.path.isfile(path) and not os.path.islink(path):
                out.append(path)
    return out


def read_files(root, count):
    files = tree_files(root)
    step = max(len(files) // count, 1)
    picked = files[::step][:count]
    start = time.perf_counter()
    for path in picked:
        with open(path, "rb") as f:
            f.read()
    return time.perf_counter() - start


def search(root, pattern):
    start = time.perf_counter()
    subprocess.run(["rg", "-c", "--no-ignore", "--hidden", "-g", "!.git", "-g", "!.gs", pattern, root], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    return time.perf_counter() - start


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--git", required=True, help="GitHub URL of the repository")
    ap.add_argument("--slice", required=True, help="account/slice with the same content")
    ap.add_argument("--runs", type=int, default=5)
    ap.add_argument("--gs", default="gs")
    ap.add_argument("--pattern", default="TODO")
    ap.add_argument("--json")
    ap.add_argument("--skip-lazy", action="store_true")
    ap.add_argument("--only", help="comma-separated words; run only scenarios whose name contains one of them")
    args = ap.parse_args()

    base = tempfile.mkdtemp(prefix="gsbench-")
    home = os.path.join(base, "home")
    os.makedirs(home)
    src_home = os.environ.get("HOME", "")
    shutil.copytree(os.path.join(src_home, ".gitslice"), os.path.join(home, ".gitslice"))
    results = {}
    try:
        def gs_env(n):
            # One mount supervisor serves a user's lazy workspaces and reads its
            # cache directory from the environment it was started in, so each
            # run gets its own runtime directory (and so its own supervisor),
            # and its own cache: nothing a run fetched is there for the next.
            runtime = os.path.join(base, f"run{n}")
            os.makedirs(runtime, exist_ok=True)
            os.chmod(runtime, 0o700)
            env = dict(os.environ, HOME=home, GS_CLIENT_CACHE_DIR=os.path.join(base, f"cache{n}"), GITSLICE_CLIENT_CACHE_DIR=os.path.join(base, f"cache{n}"), XDG_RUNTIME_DIR=runtime)
            env.pop("XDG_DATA_HOME", None)
            return env

        scenarios = {
            "git clone --depth 1": lambda d, n: run(["git", "clone", "-q", "--depth", "1", args.git, d])[0],
            "git clone (full history)": lambda d, n: run(["git", "clone", "-q", args.git, d])[0],
            "git clone --filter=blob:none": lambda d, n: run(["git", "clone", "-q", "--filter=blob:none", args.git, d])[0],
            "gs init": lambda d, n: run([args.gs, "init", args.slice], cwd=d, env=gs_env(n))[0],
        }
        if not args.skip_lazy:
            scenarios["gs init --lazy"] = lambda d, n: run([args.gs, "init", "--lazy", args.slice, d], env=gs_env(n))[0]

            def lazy_then_fetch(d, n):
                # What to do for work that reads the whole tree: mount, then
                # fetch every file in batches, in parallel, before starting.
                mounted = run([args.gs, "init", "--lazy", args.slice, d], env=gs_env(n))[0]
                return mounted + run([args.gs, "fetch"], cwd=d, env=gs_env(n))[0]

            scenarios["gs init --lazy + gs fetch"] = lazy_then_fetch
        for name, make in scenarios.items():
            if args.only and not any(word.strip() in name for word in args.only.split(",")):
                continue
            rows = []
            for n in range(args.runs):
                d = os.path.join(base, f"w{n}")
                shutil.rmtree(d, ignore_errors=True)
                os.makedirs(d, exist_ok=True) if name.startswith("gs") else None
                t_tree = make(d, n)
                root = d
                t_read = read_files(root, 20)
                t_cold = search(root, args.pattern)
                t_warm = search(root, args.pattern)
                rows.append({"tree": t_tree, "read": t_read, "cold": t_cold, "warm": t_warm})
                print(f"  {name} run {n + 1}: tree {t_tree:.2f}s read20 {t_read:.2f}s search {t_cold:.2f}s/{t_warm:.2f}s", file=sys.stderr)
                if name.startswith("gs init --lazy"):
                    run([args.gs, "unmount", d], env=gs_env(n), check=False)
                shutil.rmtree(d, ignore_errors=True)
                shutil.rmtree(os.path.join(base, f"cache{n}"), ignore_errors=True)
            results[name] = {key: statistics.median(r[key] for r in rows) for key in rows[0]}
            results[name]["runs"] = rows
        info = {"git_rtt_s": rtt(args.git), "gitslice_rtt_s": rtt("https://api.gitslice.io"), "runs": args.runs, "cpus": os.cpu_count()}
    finally:
        subprocess.run([args.gs, "mount", "list"], env=dict(os.environ, HOME=home), stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        shutil.rmtree(base, ignore_errors=True)

    print(f"\n{args.git}  vs  {args.slice}   (medians of {args.runs}; connect time to GitHub {info['git_rtt_s'] * 1000:.0f} ms, to Gitslice {info['gitslice_rtt_s'] * 1000:.0f} ms)")
    print(f"{'scenario':32} {'to tree':>9} {'read 20':>9} {'search cold':>12} {'search warm':>12}")
    for name, r in results.items():
        print(f"{name:32} {r['tree']:8.2f}s {r['read']:8.2f}s {r['cold']:11.2f}s {r['warm']:11.2f}s")
    if args.json:
        with open(args.json, "w") as f:
            json.dump({"info": info, "results": results}, f, indent=2)


if __name__ == "__main__":
    main()
