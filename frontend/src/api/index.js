import axios from 'axios';

const api = axios.create({
  baseURL: '/v1',
});

export const fetchGalleries = async (params) => {
  const { tags, ...rest } = params;
  const query = new URLSearchParams();
  for (const [k, v] of Object.entries(rest)) {
    if (v !== undefined && v !== null && v !== '') query.append(k, v);
  }
  if (Array.isArray(tags)) {
    for (const t of tags) { if (t) query.append('tag', t); }
  }
  const { data } = await api.get(`/galleries?${query.toString()}`);
  return data;
};

export const fetchStats = async () => {
  const { data } = await api.get('/stats');
  return data;
};

export const fetchGalleryGroup = async (groupId) => {
  const { data } = await api.get(`/galleries/group/${groupId}`);
  return data;
};
