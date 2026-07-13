import test from 'node:test';
import assert from 'node:assert/strict';
import { buildGalleryRequestParams } from './galleryQuery.js';

const filters = {
  category: 'Manga',
  sort: 'rating',
  min_rating: 4.5,
  min_fav: 200,
  tags: ['language:chinese'],
  is_favorited: false,
};

test('Gallery keeps gid pagination and excludes page-local controls', () => {
  const params = buildGalleryRequestParams({ filters, page: 2, pageSize: 24 });
  assert.equal(params.sort, 'gid_desc');
  assert.equal(params.min_rating, undefined);
  assert.deepEqual(params.tags, ['language:chinese']);
  assert.equal(params.min_fav, 200);
});

test('For You keeps recommendation pagination and excludes page-local controls', () => {
  const params = buildGalleryRequestParams({
    filters,
    page: 1,
    pageSize: 24,
    recommendedOnly: true,
  });
  assert.equal(params.sort, 'recommended');
  assert.equal(params.min_rating, undefined);
  assert.equal(params.is_favorited, false);
});

test('Favorites keeps gid pagination while preserving its scope', () => {
  const params = buildGalleryRequestParams({
    filters: { ...filters, is_favorited: true },
    page: 1,
    pageSize: 24,
  });
  assert.equal(params.sort, 'gid_desc');
  assert.equal(params.is_favorited, true);
  assert.equal(params.min_rating, undefined);
});
