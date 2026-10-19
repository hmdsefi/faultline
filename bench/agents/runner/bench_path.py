"""Puts bench/agents on sys.path, so the tests can import bench.py."""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
