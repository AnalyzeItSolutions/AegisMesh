import pytest

pytest.importorskip("h3")
from h3_index import cell_for, cells_in_bbox, ring, same_place


def test_cell_and_ring():
    c = cell_for(28.6139, 77.2090)
    assert len(ring(c, 1)) == 7 and c in ring(c, 1)


def test_bbox_cells_contain_inner_points():
    cells = cells_in_bbox(28.55, 77.15, 28.70, 77.30, res=7)
    assert len(cells) > 10 and cell_for(28.6139, 77.2090, 7) in cells and cell_for(40.0, 10.0, 7) not in cells


def test_same_place():
    assert same_place((28.6139, 77.2090), (28.6140, 77.2091)) and not same_place((28.6139, 77.2090), (28.70, 77.30))
