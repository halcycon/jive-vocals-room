#!/usr/bin/env python3
"""End-to-end tests: the real bridge against fake_osclive on spare ports.

Run with an interpreter that has python-osc (e.g. the mini's osclive venv):
    python -m unittest -v test_bridge_dsp
Nothing here touches hardware or the production ports.
"""

import json
import os
import socket
import subprocess
import sys
import tempfile
import time
import unittest

from pythonosc.osc_message_builder import OscMessageBuilder
from pythonosc.osc_packet import OscPacket

from fake_osclive import FakeOsclive

HERE = os.path.dirname(os.path.abspath(__file__))
BRIDGE = os.path.join(HERE, "osc_tcp1_bridge_jack.py")
SETTLE = 1.2


def free_port(kind=socket.SOCK_DGRAM):
    s = socket.socket(socket.AF_INET, kind)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def osc(addr, *args):
    b = OscMessageBuilder(address=addr)
    for a in args:
        b.add_arg(a)
    return b.build().dgram


def proposal(pid, source, changes, summary="test"):
    return json.dumps({"schema_version": "jive-room-proposal/v1", "id": pid, "source": source,
                       "summary": summary, "changes": [{"address": a, "raw": r} for a, r in changes]})


class Bridge:
    def __init__(self, state_dir, osclive_port):
        self.ui_listen = free_port()
        self.dsp_port = free_port()
        # The cache replay is a burst of ~500 datagrams; size buffers for it.
        self.ui = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.ui.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 4 << 20)
        self.ui.bind(("127.0.0.1", 0))
        self.ui.settimeout(0.02)
        self.dsp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.dsp.setsockopt(socket.SOL_SOCKET, socket.SO_RCVBUF, 4 << 20)
        self.dsp.bind(("127.0.0.1", 0))
        self.dsp.settimeout(0.02)
        env = dict(os.environ,
                   STUDIOLIVE_OSCLIVE_PORT=str(osclive_port),
                   STUDIOLIVE_UI_LISTEN_PORT=str(self.ui_listen),
                   STUDIOLIVE_UI_PORT=str(self.ui.getsockname()[1]),
                   STUDIOLIVE_DSP_PORT=str(self.dsp_port),
                   STUDIOLIVE_DSP_STATE_DIR=state_dir)
        self.log = tempfile.TemporaryFile()
        self.proc = subprocess.Popen([sys.executable, "-u", BRIDGE], env=env, stdout=self.log, stderr=subprocess.STDOUT)
        self.results, self.ui_msgs = [], []
        self.wait_ready()

    def wait_ready(self):
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            self.subscribe()
            if self.drain(until=lambda m: m.address == "/dsp/replay_done" and m.params[0] > 400, timeout=0.5):
                time.sleep(SETTLE)
                return
        raise RuntimeError("bridge did not connect to fake osclive:\n" + self.output())

    def output(self):
        self.log.seek(0)
        return self.log.read().decode(errors="replace")

    def subscribe(self):
        self.dsp.sendto(osc("/dsp/subscribe", self.dsp.getsockname()[1]), ("127.0.0.1", self.dsp_port))

    def send_dsp(self, addr, *args):
        self.dsp.sendto(osc(addr, *args), ("127.0.0.1", self.dsp_port))

    def send_ui(self, addr, *args):
        self.ui.sendto(osc(addr, *args), ("127.0.0.1", self.ui_listen))

    def drain(self, until=None, timeout=0.3):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            for sock, sink in ((self.dsp, None), (self.ui, self.ui_msgs)):
                try:
                    data, _ = sock.recvfrom(65535)
                except (socket.timeout, BlockingIOError):
                    continue
                for t in OscPacket(data).messages:
                    m = t.message
                    if sink is not None:
                        sink.append((m.address, m.params))
                    elif m.address == "/dsp/result":
                        self.results.append(json.loads(m.params[0]))
                    if until and sink is None and until(m):
                        return True
        return False

    def result(self, action, pid=None, timeout=6):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            for i, r in enumerate(self.results):
                if r["action"] == action and (pid is None or r["id"] == pid):
                    return self.results.pop(i)
            self.drain(timeout=0.1)
        raise AssertionError(f"no {action} result for {pid}; have {self.results}\n{self.output()[-2000:]}")

    def close(self):
        self.proc.terminate()
        try:
            self.proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            self.proc.kill()
        self.ui.close()
        self.dsp.close()
        self.log.close()


class BridgeDSPTest(unittest.TestCase):
    def setUp(self):
        self.state_dir = tempfile.mkdtemp()
        self.fake = FakeOsclive(free_port(socket.SOCK_STREAM))
        self.bridge = Bridge(self.state_dir, self.fake.port)
        self.fake.writes.clear()

    def tearDown(self):
        self.bridge.close()
        self.fake.close()

    def written(self):
        return [a for _, a, _ in self.fake.writes]

    def submit_apply(self, pid, source, changes):
        self.bridge.send_dsp("/dsp/proposal/set", proposal(pid, source, changes))
        self.assertEqual(self.bridge.result("submit", pid)["status"], "pending")
        self.bridge.send_dsp("/dsp/apply", pid)
        return self.bridge.result("apply", pid)

    def test_profile_apply_verify_and_undo(self):
        changes = [("/channel/ch3/eqmidgain", 102), ("/channel/ch3/eqmid", 1), ("/channel/geq0/18", 120), ("/channel/ch3/eqmidfreq", 128)]
        r = self.submit_apply("p1", "profile", changes)
        self.assertEqual(r["status"], "verified", r)
        self.assertEqual((r["written"], r["unchanged"]), (3, 1))
        self.assertEqual(sorted(self.written()), sorted(a for a, raw in changes if a != "/channel/ch3/eqmidfreq"))
        for a, raw in changes:
            self.assertEqual(self.fake.raw(a), raw, a)
        self.assertTrue(os.path.exists(os.path.join(self.state_dir, "dsp-undo.json")))
        with open(os.path.join(self.state_dir, "dsp-apply.log")) as f:
            audit = f.read().splitlines()
        self.assertEqual(json.loads(audit[-1])["status"], "verified")
        time.sleep(SETTLE)
        self.bridge.send_dsp("/dsp/revert")
        rv = self.bridge.result("revert")
        self.assertEqual(rv["status"], "verified", rv)
        self.assertEqual((self.fake.raw("/channel/ch3/eqmidgain"), self.fake.raw("/channel/ch3/eqmid"), self.fake.raw("/channel/geq0/18")), (128, 0, 127))
        time.sleep(SETTLE)
        self.bridge.send_dsp("/dsp/revert")
        self.assertEqual(self.bridge.result("revert")["reason"], "nothing to undo")

    def test_disallowed_proposals_are_rejected_without_writes(self):
        bad = [
            ("phantom", "profile", [("/channel/ch1/phantom", 1)]),
            ("fader", "profile", [("/channel/ch1/gain", 10)]),
            ("reset", "profile", [("/channel/geq0/reset", 1)]),
            ("geqen", "profile", [("/channel/geq0/enable", 1)]),
            ("main", "profile", [("/channel/main/eqmidgain", 100)]),
            ("ch13", "profile", [("/channel/ch13/eqmidgain", 100)]),
            ("range", "profile", [("/channel/ch1/eqmidgain", 300)]),
            ("switch", "profile", [("/channel/ch1/eqmid", 2)]),
            ("dup", "profile", [("/channel/ch1/eqmidgain", 100), ("/channel/ch1/eqmidgain", 90)]),
            ("rohpf", "ringout", [("/channel/ch1/hpffreq", 100)]),
            ("bad id!", "profile", [("/channel/ch1/eqmidgain", 100)]),
            ("auto", "automatic", [("/channel/ch1/eqmidgain", 100)]),
        ]
        for pid, source, changes in bad:
            self.bridge.send_dsp("/dsp/proposal/set", proposal(pid, source, changes))
            self.assertEqual(self.bridge.result("submit", pid)["status"], "rejected", pid)
        self.bridge.send_dsp("/dsp/apply", "phantom")
        self.assertEqual(self.bridge.result("apply", "phantom")["status"], "refused")
        self.assertEqual(self.written(), [])

    def test_ringout_policy(self):
        r = self.submit_apply("boost", "ringout", [("/channel/ch2/eqmidgain", 140)])
        self.assertEqual(r["status"], "refused")
        self.assertIn("only cut", r["reason"])
        r = self.submit_apply("deep", "ringout", [("/channel/ch2/eqmidgain", 90)])
        self.assertIn("exceeds", r["reason"])
        self.fake.desk_change("/channel/ch2/eqmidgain", 150 / 255)
        time.sleep(SETTLE)
        r = self.submit_apply("retune", "ringout", [("/channel/ch2/eqmidfreq", 200)])
        self.assertIn("boosts", r["reason"])
        self.assertEqual(self.written(), [])
        r = self.submit_apply("ok", "ringout", [("/channel/ch2/eqmidgain", 128), ("/channel/ch2/eqmidfreq", 200), ("/channel/ch2/eqmidhiq", 1), ("/channel/ch2/eqmid", 1)])
        self.assertEqual(r["status"], "verified", r)

    def test_settle_window_protects_fresh_desk_moves(self):
        self.fake.desk_change("/channel/ch5/eqhighgain", 100 / 255)
        r = self.submit_apply("s1", "profile", [("/channel/ch5/eqlowgain", 110)])
        self.assertEqual(r["status"], "refused")
        self.assertIn("ch5 changed", r["reason"])
        self.assertEqual(self.written(), [])
        time.sleep(SETTLE)
        self.bridge.send_dsp("/dsp/apply", "s1")
        self.assertEqual(self.bridge.result("apply", "s1")["status"], "verified")

    def test_state_updates_disconnects_and_direct_writes_never_write(self):
        for raw in (10, 50, 200, 128):
            self.fake.desk_change("/channel/ch1/eqmidgain", raw / 255)
            time.sleep(0.05)
        self.bridge.send_dsp("/channel/ch1/eqmidgain", 0.1)
        self.bridge.send_dsp("/dsp/unsubscribe", self.bridge.dsp.getsockname()[1])
        self.bridge.dsp.close()
        time.sleep(1.5)
        self.assertEqual(self.written(), [])

    def test_ui_buttons(self):
        self.bridge.send_dsp("/dsp/proposal/set", proposal("ui1", "profile", [("/channel/ch4/eqlowgain", 115)]))
        self.bridge.result("submit", "ui1")
        self.bridge.send_ui("/dsp/apply", 0.0)
        time.sleep(0.5)
        self.assertEqual(self.written(), [])
        self.bridge.send_ui("/dsp/apply", 1.0)
        self.assertEqual(self.bridge.result("apply")["status"], "verified")
        time.sleep(SETTLE)
        self.bridge.send_ui("/dsp/revert", 1.0)
        self.assertEqual(self.bridge.result("revert")["status"], "verified")
        self.assertEqual(self.fake.raw("/channel/ch4/eqlowgain"), 128)
        self.bridge.drain(timeout=0.2)
        self.assertTrue(any(a == "/dsp/ui/apply/status" for a, _ in self.bridge.ui_msgs))

    def test_ui_commands_reach_dsp_subscribers_only(self):
        self.bridge.send_ui("/dsp/cmd/channel", 3)
        self.bridge.send_ui("/dsp/cmd/capture/empty", 1.0)
        got = []
        self.bridge.drain(until=lambda m: m.address.startswith("/dsp/cmd/") and got.append((m.address, m.params)) is None and len(got) == 2, timeout=2)
        self.assertEqual(got, [("/dsp/cmd/channel", [3]), ("/dsp/cmd/capture/empty", [1.0])])
        time.sleep(0.3)
        self.assertEqual(self.written(), [])

    def auto_apply(self, pid, changes):
        self.bridge.send_dsp("/dsp/auto/apply", proposal(pid, "auto", changes))
        return self.bridge.result("auto", pid)

    def allow_auto(self, allowed=True):
        with open(os.path.join(self.state_dir, "dsp-config.json"), "w") as f:
            json.dump({"allow_auto_cuts": allowed}, f)

    def test_auto_cuts_need_config_and_operator_enable(self):
        cut = [("/channel/ch6/eqmidgain", 110)]
        self.assertIn("not allowed", self.auto_apply("a1", cut)["reason"])
        self.bridge.send_ui("/dsp/auto/enable", 1.0)
        time.sleep(0.3)
        self.assertIn("not allowed", self.auto_apply("a2", cut)["reason"])
        self.allow_auto()
        self.assertIn("not enabled", self.auto_apply("a3", cut)["reason"])
        self.bridge.send_dsp("/dsp/auto/enable", 1.0)  # the DSP port can never enable
        time.sleep(0.3)
        self.assertIn("not enabled", self.auto_apply("a4", cut)["reason"])
        self.assertEqual(self.written(), [])
        self.bridge.send_ui("/dsp/auto/enable", 1.0)
        time.sleep(0.3)
        r = self.auto_apply("a5", cut)
        self.assertEqual(r["status"], "verified", r)
        self.assertEqual(self.fake.raw("/channel/ch6/eqmidgain"), 110)
        self.bridge.send_dsp("/dsp/auto/disable")
        time.sleep(0.3)
        self.assertIn("not enabled", self.auto_apply("a6", [("/channel/ch7/eqmidgain", 110)])["reason"])
        time.sleep(SETTLE)
        self.bridge.send_dsp("/dsp/revert")
        self.assertEqual(self.bridge.result("revert")["status"], "verified")
        self.assertEqual(self.fake.raw("/channel/ch6/eqmidgain"), 128)

    def test_auto_cuts_policy_and_spacing(self):
        self.allow_auto()
        self.bridge.send_ui("/dsp/auto/enable", 1.0)
        time.sleep(0.3)
        self.assertIn("only cut", self.auto_apply("b1", [("/channel/ch2/eqmidgain", 140)])["reason"])
        self.assertIn("GEQ", self.auto_apply("b2", [("/channel/geq0/12", 110)])["reason"])
        self.assertIn("mid band", self.auto_apply("b3", [("/channel/ch2/hpffreq", 110)])["reason"])
        self.assertEqual(self.auto_apply("b4", [("/channel/ch2/eqmidgain", 110)])["status"], "verified")
        time.sleep(SETTLE)
        self.assertIn("apart", self.auto_apply("b5", [("/channel/ch3/eqmidgain", 110)])["reason"])
        self.assertEqual(len(self.written()), 1)

    def test_rate_limit(self):
        self.assertEqual(self.submit_apply("r1", "profile", [("/channel/ch7/eqlowgain", 120)])["status"], "verified")
        r = self.submit_apply("r2", "profile", [("/channel/ch8/eqlowgain", 120)])
        self.assertEqual(r["status"], "refused")
        self.assertIn("rate-limited", r["reason"])

    @unittest.skipUnless(os.environ.get("JIVE_ROOM_BIN"), "set JIVE_ROOM_BIN to a jive-room binary")
    def test_cli_profile_save_push_and_undo(self):
        bin_, addr = os.environ["JIVE_ROOM_BIN"], f"127.0.0.1:{self.bridge.dsp_port}"
        prof = os.path.join(self.state_dir, "profile.json")

        def cli(*args):
            r = subprocess.run([bin_, *args, "-studiolive-bridge", addr], capture_output=True, text=True, timeout=30)
            return r.returncode, r.stdout + r.stderr

        rc, out = cli("-studiolive-profile-save", prof, "-profile-label", "baseline")
        self.assertEqual(rc, 0, out)
        self.assertIn("nothing was written", out)
        self.fake.desk_change("/channel/ch2/eqmidgain", 90 / 255)
        self.fake.desk_change("/channel/ch2/eqmid", 1)
        self.fake.desk_change("/channel/geq0/5", 100 / 255)
        time.sleep(SETTLE)
        rc, out = cli("-studiolive-profile-push", prof)
        self.assertEqual(rc, 0, out)
        self.assertIn("raw  90 -> 128", out)
        self.assertIn("Dry run", out)
        self.assertEqual(self.written(), [])
        rc, out = cli("-studiolive-profile-push", prof, "-confirm-apply")
        self.assertEqual(rc, 0, out)
        self.assertIn("verified (3 written", out)
        self.assertEqual((self.fake.raw("/channel/ch2/eqmidgain"), self.fake.raw("/channel/ch2/eqmid"), self.fake.raw("/channel/geq0/5")), (128, 0, 127))
        rc, out = cli("-studiolive-profile-push", prof, "-confirm-apply")
        self.assertIn("already matches", out)
        time.sleep(SETTLE)
        rc, out = cli("-studiolive-undo", "-confirm-apply")
        self.assertEqual(rc, 0, out)
        self.assertIn("revert", out)
        self.assertEqual((self.fake.raw("/channel/ch2/eqmidgain"), self.fake.raw("/channel/ch2/eqmid"), self.fake.raw("/channel/geq0/5")), (90, 1, 100))
        self.assertEqual(len(self.written()), 6)

    def test_undo_survives_bridge_restart(self):
        self.assertEqual(self.submit_apply("persist", "profile", [("/channel/ch9/eqhighgain", 100)])["status"], "verified")
        self.bridge.close()
        self.bridge = Bridge(self.state_dir, self.fake.port)
        self.bridge.send_dsp("/dsp/revert")
        self.assertEqual(self.bridge.result("revert")["status"], "verified")
        self.assertEqual(self.fake.raw("/channel/ch9/eqhighgain"), 128)


if __name__ == "__main__":
    unittest.main(verbosity=2)
