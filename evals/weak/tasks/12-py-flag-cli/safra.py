#!/usr/bin/env python3
import argparse


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--sep", default=",")
    ap.add_argument("valores", nargs="*")
    args = ap.parse_args()
    linha = args.sep.join(args.valores)
    print(linha)


if __name__ == "__main__":
    main()
