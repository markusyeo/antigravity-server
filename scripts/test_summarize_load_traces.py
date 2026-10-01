import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('traces', Path(__file__).with_name('summarize-load-traces.py'))
traces = importlib.util.module_from_spec(spec)
spec.loader.exec_module(traces)


def row(event, at, elapsed=None, conversation='thread'):
    result = {'kind': 'conversation-load', 'session': 'phone', 'conversation': conversation, 'event': event, 'at': at}
    if elapsed is not None:
        result['elapsed'] = elapsed
    return result


class TraceAttemptsTest(unittest.TestCase):
    def test_malformed_records_do_not_break_a_capture(self):
        valid = dict(row('provider-created', 100, 0), ua='iPhone')
        malformed = [dict(valid, event=None), dict(valid, session=[]),
                     dict(valid, conversation={}), dict(valid, elapsed='unknown'),
                     dict(valid, at=float('nan')), dict(valid, ua=42)]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'debug.log'
            path.write_text('\n'.join(json.dumps(record) for record in malformed + [valid]))
            self.assertEqual(traces.read_records(path, phone=True), [valid])

    def test_later_failed_open_does_not_reuse_an_earlier_fast_paint(self):
        records = [row('provider-created', 100, 0), row('messages-painted', 300, 200),
                   row('provider-created', 1000, 0), row('still-waiting', 6000, 5000),
                   row('thread-click', 11000, conversation='other'), row('load-abandoned', 95000, 94000)]
        summaries = [traces.summary(load) for load in traces.attempts(records)]
        self.assertEqual(summaries[0]['elapsed_ms'], 200)
        self.assertEqual(summaries[1]['outcome'], 'left-before-paint')
        self.assertIsNone(summaries[1]['elapsed_ms'])
        self.assertEqual(summaries[1]['time_until_switch_ms'], 10000)

    def test_late_error_stays_attached_to_the_old_stream(self):
        records = [row('provider-created', 100, 0), row('stream-request', 110),
                   row('stream-headers', 140, 30), row('stream-request', 1000),
                   row('stream-headers', 3000, 2000), row('stream-read-error', 4000, 3890)]
        records[-1]['error'] = 'TypeError'
        streams = traces.summary(traces.attempts(records)[0])['streams']
        self.assertEqual(streams[0]['headers_ms'], 30)
        self.assertEqual(streams[0]['error'], 'TypeError')
        self.assertEqual(streams[1]['headers_ms'], 2000)
        self.assertIsNone(streams[1]['error'])

    def test_http_trace_delivery_order_does_not_change_load_timing(self):
        records = [row('messages-painted', 14000, 13900), row('provider-created', 150, 50),
                   row('stream-first-frame', 13800, 13600), row('stream-request', 200)]
        result = traces.summary(traces.attempts(records)[0])
        self.assertEqual(result['elapsed_ms'], 13900)
        self.assertEqual(result['streams'][0]['frame_ms'], 13600)

    def test_background_mount_is_not_reported_as_an_unqualified_slow_render(self):
        records = [dict(row('provider-created', 100, 0), visibility='visible'),
                   dict(row('messages-mounted', 600, 500), visibility='hidden'),
                   dict(row('messages-painted', 8200, 8100), visibility='visible', render=7700)]
        result = traces.summary(traces.attempts(records)[0])
        self.assertTrue(result['background_during_load'])
        self.assertEqual(result['visibility_at_mount'], 'hidden')
        self.assertEqual(result['visibility_at_paint'], 'visible')

    def test_hidden_reconnect_after_paint_does_not_reclassify_the_initial_load(self):
        records = [dict(row('provider-created', 100, 0), visibility='visible'),
                   dict(row('messages-painted', 500, 400), visibility='visible'),
                   dict(row('stream-request', 2000), visibility='hidden')]
        result = traces.summary(traces.attempts(records)[0])
        self.assertFalse(result['background_during_load'])


if __name__ == '__main__':
    unittest.main()
