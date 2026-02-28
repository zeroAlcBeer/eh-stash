const BASE = '/api/v1/admin';

async function request(path, options = {}) {
  const resp = await fetch(`${BASE}${path}`, options);
  if (!resp.ok) {
    const text = await resp.text();
    throw new Error(text || `Request failed: ${resp.status}`);
  }

  if (resp.status === 204) {
    return null;
  }

  return resp.json();
}

export function getTasks() {
  return request('/tasks');
}

export function createTask(data) {
  return request('/tasks', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data),
  });
}

export function updateTask(id, data) {
  return request(`/tasks/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data),
  });
}

export function startTask(id) {
  return request(`/tasks/${id}/start`, {
    method: 'POST',
  });
}

export function stopTask(id) {
  return request(`/tasks/${id}/stop`, {
    method: 'POST',
  });
}

export function deleteTask(id, confirm = false) {
  return request(`/tasks/${id}?confirm=${confirm ? 'true' : 'false'}`, {
    method: 'DELETE',
  });
}

export function getThumbStats() {
  return request('/thumb-queue/stats');
}
