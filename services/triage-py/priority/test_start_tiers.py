from start_tiers import BLACK, GREEN, ORDER, RED, YELLOW, Observation as O, classify


def test_the_tree():
    assert classify(O(can_walk=True)) == GREEN
    assert classify(O(breathing=False)) == BLACK
    assert classify(O(breathing=False, breathing_after_airway=True)) == RED
    assert classify(O(resp_rate=34)) == RED and classify(O(resp_rate=30)) == YELLOW
    assert classify(O(cap_refill_s=3)) == RED and classify(O(radial_pulse=False)) == RED
    assert classify(O(obeys_commands=False)) == RED
    assert classify(O(resp_rate=20, cap_refill_s=1.5, radial_pulse=True)) == YELLOW


def test_treatment_order():
    assert sorted([BLACK, GREEN, RED, YELLOW], key=ORDER.get) == [RED, YELLOW, GREEN, BLACK]
