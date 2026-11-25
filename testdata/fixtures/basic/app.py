import argparse
import os


def main():
    p = argparse.ArgumentParser(prog="pyapp")
    p.add_argument("--dry-run", action="store_true")
    print(os.environ.get("PY_SETTING"))


if __name__ == "__main__":
    main()
