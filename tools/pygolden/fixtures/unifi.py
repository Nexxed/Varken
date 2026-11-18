"""UniFi controller fixtures: /api/login (session cookie), /api/self/sites and /api/s/<site>/stat/device."""
import copy

from common import case, route

S = 'unifi'
COOKIE = 'unifises=SESSION123; Path=/; HttpOnly'
LOGIN_OK = route('/api/login', {'data': [], 'meta': {'rc': 'ok'}}, method='POST', headers={'Set-Cookie': COOKIE})
LOGIN_REQUIRED = {'data': [], 'meta': {'rc': 'error', 'msg': 'api.err.LoginRequired'}}
SITES = {'data': [{'_id': 's1', 'name': 'default', 'desc': 'Default', 'role': 'admin'},
                  {'_id': 's2', 'name': 'abc123', 'desc': 'Main Office', 'role': 'admin'}], 'meta': {'rc': 'ok'}}
SERVER = {'username': 'admin', 'password': 'secret', 'site': 'default', 'usg_name': 'MyRouter'}


def usg(name='MyRouter', model='UGW3', **overrides):
    device = {'name': name, 'model': model, 'type': 'ugw', 'mac': 'aa:bb:cc:dd:ee:ff',
              'wan1': {'bytes-r': 1500.0, 'rx_bytes': 123456789.0, 'rx_bytes-r': 1200.5, 'tx_bytes': 98765432.0,
                       'tx_bytes-r': 299.5, 'name': 'wan'},
              'sys_stats': {'loadavg_1': '0.12', 'loadavg_5': '0.10', 'loadavg_15': '0.08', 'mem_total': 2000},
              'system-stats': {'cpu': '5.3', 'mem': '41.2', 'uptime': '123456'}}
    device.update(overrides)
    return device


SWITCH = {'name': 'Office Switch', 'model': 'US24', 'type': 'usw'}


def stat_routes(devices, site='default', *, first_status=None):
    path = f'/api/s/{site}/stat/device'
    routes = []
    if first_status:
        routes.append(route(path, LOGIN_REQUIRED, status=first_status, times=1))
    # Python sends the session cookie twice ("unifises=X; unifises=X": the jar's copy plus the one it adds
    # by hand), so the cookie is checked in the Go unit tests rather than matched here.
    routes.append(route(path, {'data': devices, 'meta': {'rc': 'ok'}}))
    return routes


def full(devices, site='default', sites=SITES, **kw):
    return [LOGIN_OK, route('/api/self/sites', sites)] + stat_routes(devices, site, **kw)


case(S, 'usg_happy', 'get_usg_stats', full([SWITCH, usg()]), server=SERVER,
     description='Login cookie, site mapped by name, the named gateway among other devices.')
case(S, 'usg_site_by_desc', 'get_usg_stats', full([usg()], site='abc123'),
     server=dict(SERVER, site='main office'))
case(S, 'usg_site_not_found', 'get_usg_stats', full([usg()], site='nowhere'), server=dict(SERVER, site='nowhere'),
     description='No site matches: the configured site is used as is.')
case(S, 'usg_sites_fail', 'get_usg_stats', [LOGIN_OK, route('/api/self/sites', None, status=500, text='err')]
     + stat_routes([usg()]), server=SERVER)
case(S, 'usg_reauth', 'get_usg_stats', full([usg()], first_status=401), server=SERVER,
     description='The stats request fails once: Varken logs in again and retries once.')
case(S, 'usg_reauth_fails', 'get_usg_stats', full([usg()], first_status=None)[:2]
     + [route('/api/s/default/stat/device', LOGIN_REQUIRED, status=401)], server=SERVER)
case(S, 'usg_login_fails', 'get_usg_stats',
     [route('/api/login', {'meta': {'rc': 'error', 'msg': 'api.err.Invalid'}}, method='POST', status=400),
      route('/api/self/sites', LOGIN_REQUIRED, status=401),
      route('/api/s/default/stat/device', LOGIN_REQUIRED, status=401)], server=SERVER)
case(S, 'usg_device_not_found', 'get_usg_stats', full([SWITCH, usg(name='OtherRouter')]), server=SERVER)
case(S, 'usg_unnamed_devices', 'get_usg_stats', full([{'model': 'UAP'}, {'name': ''}, {'name': None}, usg()]),
     server=SERVER)
case(S, 'usg_duplicate_names', 'get_usg_stats',
     full([usg(model='First'), usg(model='Second')]), server=SERVER,
     description='Two devices with the gateway name: the last one wins, as in the Python dict.')

missing = usg()
del missing['system-stats']
case(S, 'usg_missing_key', 'get_usg_stats', full([missing]), server=SERVER)
null_value = usg()
null_value['sys_stats']['loadavg_1'] = None
case(S, 'usg_null_value', 'get_usg_stats', full([null_value]), server=SERVER,
     description='Python crashed on float(None); nothing is written either way (the payload is discarded).')

ints = usg()
ints['wan1'] = {'bytes-r': 1500, 'rx_bytes': 123456789, 'rx_bytes-r': 1200, 'tx_bytes': 98765432,
                'tx_bytes-r': '300'}
case(S, 'usg_int_counters', 'get_usg_stats', full([ints]), server=SERVER,
     fix='Python wrote the wan1 counters as the raw JSON numbers, ints in one poll and floats in the next, which '
         'InfluxDB rejects as a field type conflict; they are always floats.')
case(S, 'usg_server_tag', 'get_usg_stats', full([usg()]), server=dict(SERVER, add_server_tag=True),
     fix='With add_server_tag = true (new, off by default) the point carries the server tag; Python had none.')

no_desc = copy.deepcopy(SITES)
del no_desc['data'][0]['desc']
case(S, 'usg_site_without_desc', 'get_usg_stats', full([usg()], site='abc123', sites=no_desc),
     server=dict(SERVER, site='main office'),
     fix='Python crashed with KeyError at startup when a site had no description; such sites are matched by '
         'name only.')
