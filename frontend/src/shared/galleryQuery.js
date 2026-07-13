export function getGalleryBaseSort(recommendedOnly) {
  return recommendedOnly ? 'recommended' : 'gid_desc';
}

// Sorting and minimum-rating are deliberately page-local UI transforms.
// This builder is the boundary that prevents those controls from changing
// server pagination for Gallery, Favorites, or For You.
export function buildGalleryRequestParams({ filters, page, pageSize, recommendedOnly = false }) {
  const apiFilters = { ...filters };
  const { is_favorited: isFavorited, tags } = apiFilters;
  delete apiFilters.sort;
  delete apiFilters.min_rating;
  delete apiFilters.is_favorited;
  delete apiFilters.tags;

  const params = {
    page,
    page_size: pageSize,
    sort: getGalleryBaseSort(recommendedOnly),
    tags,
    ...apiFilters,
  };
  if (isFavorited) params.is_favorited = true;
  if (recommendedOnly) params.is_favorited = false;
  return params;
}
